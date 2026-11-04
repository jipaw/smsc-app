package id.smsc.agent

import id.smsc.poc.core.Backoff
import id.smsc.poc.core.Battery
import id.smsc.poc.core.Capabilities
import id.smsc.poc.core.Codec
import id.smsc.poc.core.CommandAck
import id.smsc.poc.core.CommandProcessor
import id.smsc.poc.core.DeviceCommand
import id.smsc.poc.core.DeviceCommandResult
import id.smsc.poc.core.Envelope
import id.smsc.poc.core.ErrorPayload
import id.smsc.poc.core.EventAck
import id.smsc.poc.core.Heartbeat
import id.smsc.poc.core.Hello
import id.smsc.poc.core.HelloAck
import id.smsc.poc.core.MessageType
import id.smsc.poc.core.MonotonicTime
import id.smsc.poc.core.NetworkInfoDto
import id.smsc.poc.core.OsGuard
import id.smsc.poc.core.Outbox
import id.smsc.poc.core.SimRuntime
import id.smsc.poc.core.SlotGate
import id.smsc.poc.core.SmsCancel
import id.smsc.poc.core.SmsEvent
import id.smsc.poc.core.SmsSend
import id.smsc.poc.core.SmsStatus
import id.smsc.poc.core.JobStore
import id.smsc.poc.core.WallTime
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit

/**
 * Klien protokol WS v1 yang setara GatewayClient aplikasi Android, tanpa API Android:
 * hello (+ inflight untuk rekonsiliasi) → hello.ack → heartbeat; sms.send → command.ack → event via outbox (dihapus setelah event.ack);
 * sms.cancel, sms.status → sms.state, device.command (ping, refresh_sims, rotate_credential), sambung ulang dengan backoff.
 */
class AgentClient(
    private val http: OkHttpClient,
    @Volatile private var creds: Credentials,
    private val wsUrl: String,
    private val sims: Int,
    store: JobStore,
    private val gateway: SimulatedSmsGateway,
    private val exec: java.util.concurrent.ScheduledExecutorService,
    private val saveCreds: (Credentials) -> Unit,
    private val log: (String) -> Unit,
) {
    val outbox = Outbox()
    val processor = CommandProcessor(
        store, gateway, WallTime,
        gateFactory = { SlotGate(MonotonicTime, osGuard = OsGuard(maxPerWindow = 25, windowSec = 1800)) },
        emit = { ev -> outbox.add(ev); sendEvent(ev) },
    )
    private val backoff = Backoff()
    private val io = Executors.newSingleThreadScheduledExecutor { r -> Thread(r, "agent-ws").apply { isDaemon = true } }
    @Volatile private var ws: WebSocket? = null
    @Volatile var online = false
        private set
    @Volatile var stopped = false
        private set
    @Volatile var connects = 0
        private set
    private var heartbeatFuture: ScheduledFuture<*>? = null
    private var pumpFuture: ScheduledFuture<*>? = null
    private val startedMs = System.currentTimeMillis()

    init {
        gateway.onPartSent = { id, att, part, rc -> processor.onPartSent(id, att, part, rc) }
        gateway.onPartDelivered = { id, att, part, tp -> processor.onPartDelivered(id, att, part, tp) }
    }

    fun start() { io.execute { connect() } }

    fun stop() {
        stopped = true
        heartbeatFuture?.cancel(false)
        ws?.close(1000, "stop")
        io.shutdown()
    }

    /** Putuskan koneksi secara paksa (uji sambung ulang). */
    fun dropConnection() { ws?.cancel() }

    private fun connect() {
        if (stopped) return
        log("menyambung $wsUrl")
        val req = Request.Builder().url(wsUrl)
            .header("Authorization", "Bearer ${creds.deviceSecret}")
            .header("Sec-WebSocket-Protocol", "smsc.v1")
            .build()
        ws = http.newWebSocket(req, Listener())
    }

    private fun send(env: Envelope): Boolean = ws?.send(Codec.encode(env)) ?: false
    private fun sendEvent(ev: SmsEvent) { if (online) send(Codec.envelope(MessageType.SMS_EVENT, ev)) }

    private fun simsRuntime() = (0 until sims).map { SimRuntime(it, 10 + it, "SIM-SIMULASI-$it", registered = true, signalLevel = 4, roaming = false) }

    private fun hello() = Codec.envelope(
        MessageType.HELLO,
        Hello(
            appVersion = AGENT_VERSION, protocol = 1, configVersion = 0,
            capabilities = Capabilities(batteryOptimizationIgnored = true, tailscaleVpnActive = true),
            sims = simsRuntime(), inflight = processor.inflight(), pendingEvents = outbox.size(),
        ),
    )

    private fun heartbeat() {
        if (!online) return
        send(Codec.envelope(MessageType.HEARTBEAT, Heartbeat(
            battery = Battery(90, true), network = NetworkInfoDto("wifi", true), sims = simsRuntime(),
            queueDepthLocal = processor.queueDepth(), uptimeSec = (System.currentTimeMillis() - startedMs) / 1000,
        )))
    }

    fun schedulePump(delayMs: Long) {
        synchronized(this) {
            pumpFuture?.cancel(false)
            pumpFuture = exec.schedule({
                val next = runCatching { processor.pump() }.onFailure { log("pump error: $it") }.getOrNull()
                if (next != null) schedulePump(next)
            }, delayMs, TimeUnit.MILLISECONDS)
        }
    }

    private fun onDown(socket: WebSocket, reason: String, code: Int?) {
        if (socket !== ws) return
        ws = null
        online = false
        heartbeatFuture?.cancel(false)
        log("terputus code=${code ?: "-"} $reason")
        when (code) {
            4401, 401 -> { stopped = true; log("kredensial ditolak ($code): berhenti — enroll ulang"); return } // 401 = HTTP saat upgrade
            4426 -> { stopped = true; log("protokol tidak didukung (4426): berhenti"); return }
        }
        if (stopped) return
        val delay = if (code == 4409) 30_000L else backoff.next()
        log("sambung ulang dalam ${delay}ms")
        io.schedule({ connect() }, delay, TimeUnit.MILLISECONDS)
    }

    private inner class Listener : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) {
            connects++
            webSocket.send(Codec.encode(hello()))
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            if (webSocket !== ws) return
            val env = try { Codec.decode(text) } catch (e: Exception) { log("frame tak valid: ${e.message}"); return }
            try { handle(env) } catch (e: Exception) { log("gagal menangani ${env.type}: $e") }
        }

        override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
            webSocket.close(code, reason)
            onDown(webSocket, "closing:$reason", code)
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = onDown(webSocket, "closed:$reason", code)

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) =
            onDown(webSocket, "failure:${t.javaClass.simpleName}:${t.message}", response?.code)
    }

    private fun handle(env: Envelope) {
        when (env.type) {
            MessageType.HELLO_ACK -> {
                val ack = Codec.payload<HelloAck>(env)
                online = true
                backoff.reset()
                log("online sesi=${ack.sessionId.take(8)} heartbeat=${ack.heartbeatIntervalSec}s rekonsiliasi drop=${ack.reconcile.drop.size} keep=${ack.reconcile.keep.size}")
                heartbeatFuture?.cancel(false)
                heartbeatFuture = io.scheduleWithFixedDelay({ heartbeat() }, 1, ack.heartbeatIntervalSec.coerceAtLeast(5).toLong(), TimeUnit.SECONDS)
                outbox.pending().forEach { send(Codec.envelope(MessageType.SMS_EVENT, it)) }
            }
            MessageType.SMS_SEND -> {
                val cmd = Codec.payload<SmsSend>(env)
                val ack: CommandAck = processor.onSmsSend(cmd)
                log("sms.send msg=${cmd.messageId.takeLast(8)} attempt=${cmd.attempt} slot=${cmd.sim.slotIndex} → accepted=${ack.accepted}${ack.reason?.let { " ($it)" } ?: ""}")
                send(Codec.envelope(MessageType.COMMAND_ACK, ack, ref = env.id))
                schedulePump(0)
            }
            MessageType.SMS_CANCEL -> {
                val c = Codec.payload<SmsCancel>(env)
                send(Codec.envelope(MessageType.COMMAND_ACK, processor.onSmsCancel(c.messageId), ref = env.id))
            }
            MessageType.SMS_STATUS -> {
                val q = Codec.payload<SmsStatus>(env)
                val st = processor.onSmsStatus(q)
                log("sms.status msg=${q.messageId.takeLast(8)} → ${st.state}")
                send(Codec.envelope(MessageType.SMS_STATE, st, ref = env.id))
            }
            MessageType.EVENT_ACK -> outbox.ack(Codec.payload<EventAck>(env).eventIds)
            MessageType.DEVICE_COMMAND -> {
                val c = Codec.payload<DeviceCommand>(env)
                send(Codec.envelope(MessageType.COMMAND_ACK, CommandAck(true, null), ref = env.id))
                val result = when (c.action) {
                    "ping", "refresh_sims" -> DeviceCommandResult(c.commandId, true)
                    "rotate_credential" -> {
                        val secret = c.newSecret
                        if (secret.isNullOrEmpty()) DeviceCommandResult(c.commandId, false, "new_secret kosong")
                        else runCatching {
                            // simpan dahulu (setara Keystore), baru konfirmasi: secret lama tetap berlaku sampai server menerima ok
                            val next = creds.copy(deviceSecret = secret)
                            saveCreds(next); creds = next
                            DeviceCommandResult(c.commandId, true)
                        }.getOrElse { DeviceCommandResult(c.commandId, false, "gagal menyimpan: ${it.javaClass.simpleName}") }
                    }
                    else -> DeviceCommandResult(c.commandId, false, "tidak didukung")
                }
                log("device.command ${c.action} → ok=${result.ok}")
                send(Codec.envelope(MessageType.DEVICE_COMMAND_RESULT, result))
            }
            MessageType.CONFIG_UPDATE -> send(Codec.envelope(MessageType.COMMAND_ACK, CommandAck(true, null), ref = env.id))
            MessageType.ERROR -> log("error dari server: ${Codec.payload<ErrorPayload>(env)}")
            else -> log("type tak dikenal diabaikan: ${env.type}")
        }
    }
}

const val AGENT_VERSION = "agent-0.1"
