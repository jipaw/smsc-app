package id.smsc.poc.core

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.encodeToJsonElement
import java.time.Instant
import kotlin.random.Random

/** Model pesan WebSocket v1 (lihat contracts/device-ws-protocol.md dan contracts/ws-schema.json). */
val ProtocolJson: Json = Json {
    ignoreUnknownKeys = true   // forward-compatible: field tak dikenal diabaikan
    encodeDefaults = true      // `ref: null` dll. tetap dikirim sesuai kontrak
}

@Serializable
data class Envelope(
    val v: Int = 1,
    val type: String,
    val id: String,
    val ref: String? = null,
    val ts: String,
    val payload: JsonObject = JsonObject(emptyMap()),
)

@Serializable
data class Capabilities(
    @SerialName("default_sms_app") val defaultSmsApp: Boolean = false,
    @SerialName("os_sms_limit_unlocked") val osSmsLimitUnlocked: Boolean = false,
    @SerialName("battery_optimization_ignored") val batteryOptimizationIgnored: Boolean = false,
    @SerialName("tailscale_vpn_active") val tailscaleVpnActive: Boolean = false,
)

@Serializable
data class SimRuntime(
    @SerialName("slot_index") val slotIndex: Int,
    @SerialName("subscription_id") val subscriptionId: Int? = null,
    @SerialName("carrier_name") val carrierName: String? = null,
    val registered: Boolean? = null,
    @SerialName("signal_level") val signalLevel: Int? = null,
    val roaming: Boolean? = null,
)

@Serializable
data class Inflight(
    @SerialName("message_id") val messageId: String,
    val attempt: Int,
    val state: String,
    @SerialName("parts_total") val partsTotal: Int? = null,
    @SerialName("parts_sent") val partsSent: Int? = null,
    @SerialName("parts_delivered") val partsDelivered: Int? = null,
)

@Serializable
data class Hello(
    @SerialName("app_version") val appVersion: String,
    val protocol: Int = 1,
    @SerialName("config_version") val configVersion: Int = 0,
    val capabilities: Capabilities = Capabilities(),
    val sims: List<SimRuntime> = emptyList(),
    val inflight: List<Inflight> = emptyList(),
    @SerialName("pending_events") val pendingEvents: Int = 0,
)

@Serializable
data class Reconcile(val drop: List<String> = emptyList(), val keep: List<String> = emptyList())

@Serializable
data class HelloAck(
    @SerialName("session_id") val sessionId: String,
    @SerialName("heartbeat_interval_sec") val heartbeatIntervalSec: Int = 30,
    @SerialName("config_version") val configVersion: Int = 0,
    val config: JsonObject? = null,
    val reconcile: Reconcile = Reconcile(),
)

@Serializable
data class Battery(val level: Int, val charging: Boolean, @SerialName("temperature_c") val temperatureC: Double? = null)

@Serializable
data class NetworkInfoDto(
    val type: String,
    @SerialName("tailscale_vpn_active") val tailscaleVpnActive: Boolean,
    @SerialName("rtt_ms") val rttMs: Int? = null,
)

@Serializable
data class Heartbeat(
    val battery: Battery,
    val network: NetworkInfoDto,
    val sims: List<SimRuntime> = emptyList(),
    @SerialName("queue_depth_local") val queueDepthLocal: Int = 0,
    @SerialName("clock_skew_ms") val clockSkewMs: Long? = null,
    @SerialName("uptime_sec") val uptimeSec: Long? = null,
)

@Serializable
data class SimRef(
    @SerialName("slot_index") val slotIndex: Int,
    @SerialName("subscription_id_hint") val subscriptionIdHint: Int? = null,
)

@Serializable
data class SmsSend(
    @SerialName("message_id") val messageId: String,
    val attempt: Int,
    val sim: SimRef,
    val to: String,
    val body: String,
    @SerialName("delivery_report") val deliveryReport: Boolean,
    @SerialName("not_after") val notAfter: String,
    @SerialName("min_gap_ms") val minGapMs: Long? = null,
)

@Serializable
data class SmsCancel(@SerialName("message_id") val messageId: String)

@Serializable
data class CommandAck(val accepted: Boolean, val reason: String? = null)

@Serializable
data class SmsEvent(
    @SerialName("event_id") val eventId: String,
    @SerialName("message_id") val messageId: String,
    val attempt: Int,
    val state: String,
    @SerialName("part_index") val partIndex: Int? = null,
    @SerialName("parts_total") val partsTotal: Int,
    @SerialName("error_code") val errorCode: String? = null,
    @SerialName("android_result_code") val androidResultCode: Int? = null,
    val at: String,
)

@Serializable
data class EventAck(@SerialName("event_ids") val eventIds: List<String>)

@Serializable
data class DeviceCommand(
    @SerialName("command_id") val commandId: String,
    val action: String,
    /** Hanya untuk rotate_credential; jangan pernah dicatat di log. */
    @SerialName("new_secret") val newSecret: String? = null,
) {
    override fun toString() = "DeviceCommand(commandId=$commandId, action=$action, newSecret=${if (newSecret != null) "***" else "null"})"
}

@Serializable
data class DeviceCommandResult(
    @SerialName("command_id") val commandId: String,
    val ok: Boolean,
    val detail: String? = null,
)

@Serializable
data class ErrorPayload(val code: String, val message: String, val fatal: Boolean = false)

object MessageType {
    const val HELLO = "hello"
    const val HELLO_ACK = "hello.ack"
    const val HEARTBEAT = "heartbeat"
    const val SMS_SEND = "sms.send"
    const val SMS_CANCEL = "sms.cancel"
    const val COMMAND_ACK = "command.ack"
    const val SMS_EVENT = "sms.event"
    const val EVENT_ACK = "event.ack"
    const val CONFIG_UPDATE = "config.update"
    const val DEVICE_COMMAND = "device.command"
    const val DEVICE_COMMAND_RESULT = "device.command_result"
    const val ERROR = "error"
}

object Codec {
    fun decode(text: String): Envelope = ProtocolJson.decodeFromString(Envelope.serializer(), text)
    fun encode(e: Envelope): String = ProtocolJson.encodeToString(Envelope.serializer(), e)

    inline fun <reified T> payload(e: Envelope): T = ProtocolJson.decodeFromJsonElement<T>(e.payload)

    inline fun <reified T> envelope(
        type: String,
        payload: T,
        ref: String? = null,
        id: String = Ids.v7(),
        nowMs: Long = System.currentTimeMillis(),
    ): Envelope = Envelope(
        type = type, id = id, ref = ref, ts = Time.iso(nowMs),
        payload = ProtocolJson.encodeToJsonElement(payload) as JsonObject,
    )
}

object Time {
    fun iso(ms: Long): String = Instant.ofEpochMilli(ms).toString()
    fun parseMs(iso: String): Long? = runCatching { Instant.parse(iso).toEpochMilli() }.getOrNull()
}

object Ids {
    /** UUIDv7 (48-bit timestamp ms + acak). */
    fun v7(nowMs: Long = System.currentTimeMillis(), rnd: Random = Random.Default): String {
        val hi = (nowMs shl 16) or 0x7000L or (rnd.nextLong() and 0x0FFFL)
        val lo = (rnd.nextLong() and 0x3FFFFFFFFFFFFFFFL) or (1L shl 63)
        return String.format(
            "%08x-%04x-%04x-%04x-%012x",
            hi ushr 32, (hi ushr 16) and 0xFFFF, hi and 0xFFFF, lo ushr 48, lo and 0xFFFFFFFFFFFFL,
        )
    }
}
