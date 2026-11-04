package id.smsc.agent

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.add
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonObject
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.net.URI
import java.net.URLDecoder
import java.util.UUID

/**
 * Isi QR enrollment: `smsc://enroll?s=<server>&t=<token>[&k=<auth key tailnet>]` (sama dengan yang dipindai aplikasi).
 * `k` = auth key Tailscale sekali pakai yang dibuat server; aplikasi memakainya untuk menyalakan tailnet ter-embed sebelum enroll.
 */
data class EnrollLink(val server: String, val token: String, val tailnetKey: String? = null) {
    override fun toString() = "EnrollLink(server=$server, token=***, tailnetKey=${if (tailnetKey != null) "***" else "null"})"
    companion object {
        fun parse(link: String, serverFallback: String?): EnrollLink {
            if (link.startsWith("smsc://")) {
                val q = URI(link).rawQuery ?: error("tautan enrollment tanpa query")
                val params = q.split("&").mapNotNull { kv ->
                    val i = kv.indexOf('='); if (i < 0) null else kv.substring(0, i) to URLDecoder.decode(kv.substring(i + 1), Charsets.UTF_8)
                }.toMap()
                return EnrollLink(params["s"] ?: error("tautan tanpa s=<server>"), params["t"] ?: error("tautan tanpa t=<token>"), params["k"])
            }
            // token / short_code manual
            return EnrollLink(serverFallback ?: error("--server wajib bila memakai kode manual"), link)
        }
    }
}

object Enroller {
    fun enroll(http: OkHttpClient, link: EnrollLink, sims: Int, appVersion: String): Credentials {
        val installId = UUID.randomUUID().toString()
        val body = buildJsonObject {
            put("token", link.token)
            put("install_id", installId)
            putJsonObject("device_info") {
                put("manufacturer", "JVM"); put("model", "smsc-agent (simulasi)"); put("android_version", "14")
                put("sdk_int", 34); put("app_version", appVersion)
            }
            putJsonObject("capabilities") { put("battery_optimization_ignored", true); put("tailscale_vpn_active", true) }
            put("sims", buildJsonArray {
                for (i in 0 until sims) add(buildJsonObject {
                    put("slot_index", i); put("subscription_id", 10 + i); put("carrier_name", "SIM-SIMULASI-$i")
                    put("registered", true); put("signal_level", 4); put("roaming", false)
                })
            })
        }
        val req = Request.Builder().url(link.server.trimEnd('/') + "/api/v1/device/enroll")
            .post(body.toString().toRequestBody("application/json".toMediaType())).build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) error("enroll gagal: HTTP ${resp.code} $text")
            val o = Json.parseToJsonElement(text) as JsonObject
            return Credentials(
                deviceId = o["device_id"]!!.jsonPrimitive.content,
                deviceSecret = o["device_secret"]!!.jsonPrimitive.content,
                wsUrl = o["ws_url"]!!.jsonPrimitive.content,
                installId = installId,
            )
        }
    }
}
