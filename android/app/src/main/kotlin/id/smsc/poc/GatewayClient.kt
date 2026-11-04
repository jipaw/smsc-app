package id.smsc.poc

import android.content.Context
import android.os.SystemClock
import id.smsc.poc.core.Backoff
import id.smsc.poc.core.CommandAck
import id.smsc.poc.core.Codec
import id.smsc.poc.core.DeviceCommand
import id.smsc.poc.core.DeviceCommandResult
import id.smsc.poc.core.Envelope
import id.smsc.poc.core.ErrorPayload
import id.smsc.poc.core.EventAck
import id.smsc.poc.core.Heartbeat
import id.smsc.poc.core.Hello
import id.smsc.poc.core.HelloAck
import id.smsc.poc.core.MessageType
import id.smsc.poc.core.ProxySpec
import id.smsc.poc.core.SmsCancel
import id.smsc.poc.core.SmsStatus
import id.smsc.poc.core.SmsEvent
import id.smsc.poc.core.SmsSend
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit

/**
 * Klien WebSocket protokol v1 (device-ws-protocol.md). Mencatat "ledger" koneksi untuk analisis R3:
 * kapan tersambung/putus, durasi, dan penyebab.
 */
class GatewayClient(private val ctx: Context, private val url: String, @Volatile private var token: String, private val proxy: ProxySpec? = null) {
    // proxy: SOCKS5 loopback dari tailnet ter-embed (TailnetRuntime) — nama MagicDNS di-resolve oleh node tailnet
    private val http = OkHttpClient.Builder().pingInterval(20, TimeUnit.SECONDS).readTimeout(0, TimeUnit.MILLISECONDS).applyProxy(proxy).build()
    private val exec = Executors.newSingleThreadScheduledExecutor { r -> Thread(r, "poc-ws").apply { isDaemon = true } }
    private val backoff = Backoff()

    @Volatile private var ws: WebSocket? = null
    @Volatile private var stopped = false
    @Volatile private var connecting = false
    @Volatile var helloAcked = false
        private set
    @Volatile var connectedSinceElapsed = 0L
        private set
    private var reconnectFuture: ScheduledFuture<*>? = null
    private var heartbeatFuture: ScheduledFuture<*>? = null
    private var heartbeatSec = 30

    // ledger
    @Volatile var connectCount = 0
        private set
    @Volatile var disconnectCount = 0
        private set
    @Volatile var longestUptimeMs = 0L
        private set
    @Volatile var lastReason = "-"
        private set
    private val startedElapsed = SystemClock.elapsedRealtime()

    private val eventListener: (SmsEvent) -> Unit = { ev ->
        if (helloAcked) send(Codec.envelope(MessageType.SMS_EVENT, ev))
    }

    fun start() {
        stopped = false
        PocRuntime.listeners.add(eventListener)
        ledger("start", if (proxy != null) "$url via $proxy" else url)
        exec.execute { connect() }
    }

    fun stop() {
        stopped = true
        PocRuntime.listeners.remove(eventListener)
        reconnectFuture?.cancel(false); heartbeatFuture?.cancel(false)
        ws?.close(1000, "stop")
        ledger("stop", "uptime=${currentUptime()}ms")
        exec.shutdown()
    }

    /** Dipanggil saat jaringan/VPN kembali: sambung ulang tanpa menunggu backoff. */
    fun connectNow(reason: String) {
        if (stopped || helloAcked || connecting) return
        ledger("reconnect_trigger", reason)
        reconnectFuture?.cancel(false)
        exec.execute { connect() }
    }

    fun summary(): String {
        val state = when { helloAcked -> "ONLINE" ; connecting -> "connecting" ; stopped -> "stopped" ; else -> "offline" }
        val up = if (helloAcked) "${currentUptime() / 1000}s" else "-"
        val total = (SystemClock.elapsedRealtime() - startedElapsed) / 1000
        return "state=$state uptime_sesi=$up connect=$connectCount disconnect=$disconnectCount " +
            "uptime_terpanjang=${longestUptimeMs / 1000}s sejak_start=${total}s alasan_terakhir=$lastReason"
    }

    // ---- internal ----
    private fun currentUptime() = if (connectedSinceElapsed > 0 && helloAcked) SystemClock.elapsedRealtime() - connectedSinceElapsed else 0L

    private fun connect() {
        if (stopped || connecting) return
        connecting = true
        ledger("connecting", url)
        val req = Request.Builder().url(url)
            .header("Authorization", "Bearer $token")
            .header("Sec-WebSocket-Protocol", "smsc.v1")
            .build()
        ws = http.newWebSocket(req, Listener())
    }

    private fun send(env: Envelope): Boolean = ws?.send(Codec.encode(env)) ?: false

    private fun buildHello(): Envelope = Codec.envelope(
        MessageType.HELLO,
        Hello(
            appVersion = "poc-0.1", protocol = 1, configVersion = 0,
            capabilities = DeviceChecks.capabilities(ctx), sims = SimReader.runtime(ctx),
            inflight = PocRuntime.processor.inflight(), pendingEvents = PocRuntime.outbox.size(),
        ),
    )

    private fun heartbeat() {
        if (!helloAcked) return
        val hb = Heartbeat(
            battery = DeviceChecks.battery(ctx), network = DeviceChecks.network(ctx), sims = SimReader.runtime(ctx),
            queueDepthLocal = PocRuntime.processor.queueDepth(), uptimeSec = currentUptime() / 1000,
        )
        send(Codec.envelope(MessageType.HEARTBEAT, hb))
        PocLog.i("hb", "bat=${hb.battery.level}% ${hb.battery.charging} net=${hb.network.type} vpn=${hb.network.tailscaleVpnActive} up=${hb.uptimeSec}s")
        PocService.updateNotification(ctx, summary())
    }

    private fun onDown(socket: WebSocket, reason: String, code: Int?) {
        if (socket !== ws) return
        ws = null
        val wasOnline = helloAcked
        val up = currentUptime()
        helloAcked = false; connecting = false
        heartbeatFuture?.cancel(false)
        if (wasOnline) { disconnectCount++; if (up > longestUptimeMs) longestUptimeMs = up }
        lastReason = reason
        ledger("disconnected", "code=${code ?: "-"} reason=$reason uptime=${up}ms")
        // 4401 = close code (di-revoke saat tersambung); 401 = HTTP saat upgrade (secret tidak valid) — keduanya fatal (protokol §1, §6)
        if (code == 4401 || code == 401) { stopped = true; ledger("revoked", "kredensial ditolak ($code), berhenti"); return }
        if (code == 4426) { stopped = true; ledger("version", "protokol tidak didukung (4426), berhenti"); return }
        if (stopped) return
        val delay = if (code == 4409) 30_000L else backoff.next()
        ledger("backoff", "${delay}ms")
        reconnectFuture = exec.schedule({ connect() }, delay, TimeUnit.MILLISECONDS)
    }

    private fun ledger(event: String, detail: String) {
        PocLog.i("ws", "$event $detail")
        PocLog.csv(
            "ledger.csv", "ts_ms,elapsed_s,event,detail",
            "${System.currentTimeMillis()},${(SystemClock.elapsedRealtime() - startedElapsed) / 1000},$event,\"${detail.replace('"', '\'')}\"",
        )
    }

    private inner class Listener : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) {
            connecting = false
            connectCount++
            ledger("open", "http=${response.code} proto=${response.header("Sec-WebSocket-Protocol")}")
            webSocket.send(Codec.encode(buildHello()))
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            if (webSocket !== ws) return
            val env = try { Codec.decode(text) } catch (e: Exception) { PocLog.i("ws", "frame tak valid: ${e.message}"); return }
            try { handle(env) } catch (e: Exception) { PocLog.i("ws", "gagal menangani ${env.type}: $e") }
        }

        override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
            webSocket.close(code, reason)
            onDown(webSocket, "closing:$reason", code)
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = onDown(webSocket, "closed:$reason", code)

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) =
            onDown(webSocket, "failure:${t.javaClass.simpleName}:${t.message}", response?.code)
    }

    /** Rotasi kredensial: simpan secret baru di Keystore DULU, baru balas ok (secret lama tetap sah sampai server menerima ok). */
    private fun rotate(c: DeviceCommand): DeviceCommandResult {
        val ns = c.newSecret
        val cur = CredentialStore.load(ctx)
        return when {
            ns.isNullOrEmpty() -> DeviceCommandResult(c.commandId, false, "new_secret kosong")
            cur == null -> DeviceCommandResult(c.commandId, false, "device tidak terdaftar lewat QR (setelan manual)")
            !CredentialStore.save(ctx, cur.copy(deviceSecret = ns)) -> DeviceCommandResult(c.commandId, false, "gagal menyimpan di Keystore")
            else -> { token = ns; DeviceCommandResult(c.commandId, true) }
        }
    }

    private fun handle(env: Envelope) {
        when (env.type) {
            MessageType.HELLO_ACK -> {
                val ack = Codec.payload<HelloAck>(env)
                heartbeatSec = ack.heartbeatIntervalSec.coerceAtLeast(5)
                helloAcked = true
                connectedSinceElapsed = SystemClock.elapsedRealtime()
                backoff.reset()
                ledger("online", "session=${ack.sessionId.take(8)} hb=${heartbeatSec}s drop=${ack.reconcile.drop.size} keep=${ack.reconcile.keep.size}")
                heartbeatFuture?.cancel(false)
                heartbeatFuture = exec.scheduleWithFixedDelay({ heartbeat() }, 1, heartbeatSec.toLong(), TimeUnit.SECONDS)
                PocRuntime.outbox.pending().forEach { send(Codec.envelope(MessageType.SMS_EVENT, it)) }   // flush outbox
            }
            MessageType.SMS_SEND -> {
                val cmd = Codec.payload<SmsSend>(env)
                val ack: CommandAck = PocRuntime.processor.onSmsSend(cmd)
                PocLog.i("ws", "sms.send msg=${cmd.messageId.take(8)} slot=${cmd.sim.slotIndex} -> accepted=${ack.accepted} ${ack.reason ?: ""}")
                send(Codec.envelope(MessageType.COMMAND_ACK, ack, ref = env.id))
                PocRuntime.schedulePump(0)
            }
            MessageType.SMS_CANCEL -> {
                val c = Codec.payload<SmsCancel>(env)
                send(Codec.envelope(MessageType.COMMAND_ACK, PocRuntime.processor.onSmsCancel(c.messageId), ref = env.id))
            }
            MessageType.SMS_STATUS -> {
                val q = Codec.payload<SmsStatus>(env)
                send(Codec.envelope(MessageType.SMS_STATE, PocRuntime.processor.onSmsStatus(q), ref = env.id))
            }
            MessageType.EVENT_ACK -> PocRuntime.outbox.ack(Codec.payload<EventAck>(env).eventIds)
            MessageType.DEVICE_COMMAND -> {
                val c = Codec.payload<DeviceCommand>(env)
                send(Codec.envelope(MessageType.COMMAND_ACK, CommandAck(true, null), ref = env.id))
                val result = when (c.action) {
                    "ping", "refresh_sims" -> DeviceCommandResult(c.commandId, true)
                    "rotate_credential" -> rotate(c)
                    else -> DeviceCommandResult(c.commandId, false, "tidak didukung di PoC")
                }
                ledger("command", "${c.action} -> ok=${result.ok}")
                send(Codec.envelope(MessageType.DEVICE_COMMAND_RESULT, result))
            }
            MessageType.CONFIG_UPDATE -> send(Codec.envelope(MessageType.COMMAND_ACK, CommandAck(true, null), ref = env.id))
            MessageType.ERROR -> PocLog.i("ws", "error dari server: ${Codec.payload<ErrorPayload>(env)}")
            else -> PocLog.i("ws", "type tak dikenal diabaikan: ${env.type}")
        }
    }
}
