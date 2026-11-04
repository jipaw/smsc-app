package id.smsc.poc.core

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonPrimitive
import java.net.URI
import java.net.URLDecoder
import java.util.UUID

/**
 * Isi QR enrollment: `smsc://enroll?s=<server>&t=<token>[&k=<auth key tailnet>]`, atau kode manual + URL server.
 * `k` = auth key Tailscale sekali pakai buatan server: dipakai untuk menyalakan tailnet ter-embed SEBELUM enroll.
 */
data class EnrollLink(val server: String, val token: String, val tailnetKey: String? = null) {
    override fun toString() = "EnrollLink(server=$server, token=***, tailnetKey=${if (tailnetKey != null) "***" else "null"})"

    /** Host server untuk konfirmasi pengguna (tautan dari luar aplikasi dapat menunjuk server penyerang). */
    val serverHost: String get() = runCatching { URI(server).host }.getOrNull() ?: server

    companion object {
        fun parse(raw: String, serverFallback: String?): EnrollLink {
            val link = raw.trim()
            if (link.startsWith("smsc://", ignoreCase = true)) {
                val uri = try { URI(link) } catch (e: Exception) { throw IllegalArgumentException("tautan enrollment rusak") }
                if (!uri.host.equals("enroll", ignoreCase = true)) throw IllegalArgumentException("bukan tautan enrollment SMSC")
                val q = uri.rawQuery ?: throw IllegalArgumentException("tautan enrollment tanpa query")
                val params = q.split("&").mapNotNull { kv ->
                    val i = kv.indexOf('='); if (i < 0) null else kv.substring(0, i) to URLDecoder.decode(kv.substring(i + 1), Charsets.UTF_8)
                }.toMap()
                val server = params["s"]?.trimEnd('/') ?: throw IllegalArgumentException("tautan tanpa s=<server>")
                checkServer(server)
                return EnrollLink(server, params["t"]?.takeIf { it.isNotBlank() } ?: throw IllegalArgumentException("tautan tanpa t=<token>"),
                    params["k"]?.takeIf { it.isNotBlank() })
            }
            if (link.contains("://")) throw IllegalArgumentException("bukan tautan enrollment SMSC (harus diawali smsc://enroll)")
            // token / short_code manual (mis. "ABCD-1234")
            val server = serverFallback?.trim()?.trimEnd('/')?.takeIf { it.isNotEmpty() } ?: throw IllegalArgumentException("URL server wajib diisi untuk kode manual")
            checkServer(server)
            if (link.isEmpty() || link.any { it.isWhitespace() }) throw IllegalArgumentException("kode enrollment tidak valid")
            return EnrollLink(server, link)
        }

        private fun checkServer(s: String) {
            val u = runCatching { URI(s) }.getOrNull()
            if (u == null || (u.scheme != "https" && u.scheme != "http") || u.host.isNullOrEmpty()) throw IllegalArgumentException("URL server harus http(s)://host")
        }
    }
}

@Serializable
data class DeviceInfoDto(
    val manufacturer: String,
    val model: String,
    @SerialName("android_version") val androidVersion: String? = null,
    @SerialName("sdk_int") val sdkInt: Int,
    @SerialName("app_version") val appVersion: String,
)

@Serializable
data class SimReport(
    @SerialName("slot_index") val slotIndex: Int,
    @SerialName("subscription_id") val subscriptionId: Int? = null,
    @SerialName("carrier_name") val carrierName: String? = null,
    @SerialName("display_name") val displayName: String? = null,
    @SerialName("phone_number") val phoneNumber: String? = null,
    @SerialName("is_esim") val isEsim: Boolean = false,
    @SerialName("signal_level") val signalLevel: Int? = null,
    val registered: Boolean? = null,
    val roaming: Boolean? = null,
)

/** Badan POST /api/v1/device/enroll (contracts/openapi.yaml DeviceEnrollRequest). */
@Serializable
data class EnrollRequest(
    val token: String,
    @SerialName("install_id") val installId: String,
    @SerialName("device_info") val deviceInfo: DeviceInfoDto,
    val capabilities: Capabilities = Capabilities(),
    val sims: List<SimReport> = emptyList(),
)

/** Kredensial hasil enrollment (disimpan aman oleh pemanggil: Keystore di HP, berkas 0600 di agen). */
@Serializable
data class DeviceCredentials(
    val deviceId: String,
    val deviceSecret: String,
    val wsUrl: String,
    val installId: String,
    val viaTailnet: Boolean = false,
) {
    override fun toString() = "DeviceCredentials(deviceId=$deviceId, deviceSecret=***, wsUrl=$wsUrl, viaTailnet=$viaTailnet)"
}

/** Kirim JSON; kembalikan (status HTTP, badan). Implementasi: OkHttp di aplikasi dan agen. */
fun interface EnrollTransport {
    fun postJson(url: String, json: String, proxy: ProxySpec?): Pair<Int, String>
}

/** Menyalakan tailnet ter-embed dengan auth key dari QR; kembalikan proxy SOCKS5-nya. */
fun interface TailnetStarter {
    fun start(authKey: String): ProxySpec
}

class EnrollException(message: String, val problemType: String? = null) : Exception(message)

/**
 * Alur enrollment yang sama untuk aplikasi Android dan agen JVM:
 * 1. QR memuat `k` dan build punya tailnet ter-embed → nyalakan tailnet dulu (server mungkin hanya terjangkau lewat tailnet);
 * 2. POST /api/v1/device/enroll (lewat proxy tailnet bila ada);
 * 3. 403 `device.tailnet_tag`/`device.tailnet_required` lewat tailnet → coba ulang sebentar (tag node baru tersebar ke server
 *    secara asinkron; penolakan tidak menghanguskan token).
 */
class EnrollFlow(
    private val transport: EnrollTransport,
    private val tailnet: TailnetStarter?,
    private val log: (String) -> Unit = {},
    private val retryDelayMs: Long = 2_000,
    private val retries: Int = 5,
    private val sleep: (Long) -> Unit = { Thread.sleep(it) },
) {
    data class Result(val credentials: DeviceCredentials, val proxy: ProxySpec?)

    fun run(link: EnrollLink, request: (installId: String) -> EnrollRequest, installId: String = UUID.randomUUID().toString(), proxy: ProxySpec? = null): Result {
        var px = proxy
        var viaTailnet = false
        if (link.tailnetKey != null) {
            if (tailnet != null) {
                log("menyalakan tailnet ter-embed dengan auth key dari QR")
                px = try { tailnet.start(link.tailnetKey) } catch (e: Exception) { throw EnrollException("tailnet gagal tersambung: ${e.message}") }
                viaTailnet = true
            } else {
                log("QR memuat auth key tailnet, tetapi build ini tanpa tailnet ter-embed: memakai jaringan HP (aplikasi Tailscale harus aktif)")
            }
        }
        val body = ProtocolJson.encodeToString(EnrollRequest.serializer(), request(installId).copy(token = link.token, installId = installId))
        val url = link.server + "/api/v1/device/enroll"
        var attempt = 0
        while (true) {
            val (code, text) = try { transport.postJson(url, body, px) } catch (e: Exception) {
                throw EnrollException("server ${link.serverHost} tidak terjangkau: ${e.javaClass.simpleName}${e.message?.let { ": $it" } ?: ""}")
            }
            if (code == 201) {
                val o = runCatching { ProtocolJson.parseToJsonElement(text) as JsonObject }.getOrNull() ?: throw EnrollException("respons enroll tidak valid")
                fun f(k: String) = o[k]?.jsonPrimitive?.content ?: throw EnrollException("respons enroll tanpa $k")
                return Result(DeviceCredentials(f("device_id"), f("device_secret"), f("ws_url"), installId, viaTailnet), px)
            }
            val type = runCatching { (ProtocolJson.parseToJsonElement(text) as JsonObject)["type"]?.jsonPrimitive?.content }.getOrNull()
            if (code == 403 && viaTailnet && (type == "device.tailnet_tag" || type == "device.tailnet_required") && attempt < retries) {
                attempt++
                log("server belum mengenali node tailnet ini ($type), coba lagi ($attempt/$retries)")
                sleep(retryDelayMs)
                continue
            }
            throw EnrollException(message(code, type), type)
        }
    }

    private fun message(code: Int, type: String?): String = when {
        code == 401 -> "kode/QR tidak valid, kedaluwarsa, atau sudah dipakai — minta QR baru dari dashboard"
        type == "device.tailnet_required" -> "server hanya menerima device lewat tailnet; QR harus memuat auth key atau aktifkan Tailscale"
        type == "device.tailnet_tag" -> "node tailnet HP ini belum ber-tag device (tag:smsc-device)"
        code == 422 -> "data device ditolak server (validasi)"
        code == 429 -> "terlalu banyak percobaan, tunggu sebentar"
        else -> "enroll gagal: HTTP $code${type?.let { " ($it)" } ?: ""}"
    }
}
