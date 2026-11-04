package id.smsc.agent

import id.smsc.poc.core.Capabilities
import id.smsc.poc.core.DeviceInfoDto
import id.smsc.poc.core.EnrollFlow
import id.smsc.poc.core.EnrollLink
import id.smsc.poc.core.EnrollRequest
import id.smsc.poc.core.EnrollTransport
import id.smsc.poc.core.ProxySpec
import id.smsc.poc.core.SimReport
import id.smsc.poc.core.TailnetStarter
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

/** Transport OkHttp untuk EnrollFlow (sama dengan aplikasi: OkHttpEnrollTransport). */
class OkHttpEnrollTransport(private val base: OkHttpClient) : EnrollTransport {
    override fun postJson(url: String, json: String, proxy: ProxySpec?): Pair<Int, String> {
        val http = if (proxy != null) base.newBuilder().applyProxy(proxy).build() else base
        val req = Request.Builder().url(url).post(json.toRequestBody("application/json".toMediaType())).build()
        http.newCall(req).execute().use { return it.code to (it.body?.string() ?: "") }
    }
}

object Enroller {
    /**
     * Enroll lewat EnrollFlow (:core) — alur yang sama dengan aplikasi Android. Agen JVM tidak dapat menjalankan tsnet,
     * jadi "tailnet ter-embed" diwakili proxy SOCKS5 node yang sudah tersambung (--proxy): bila QR memuat k=, starter
     * mengembalikan proxy itu.
     */
    fun enroll(http: OkHttpClient, link: EnrollLink, sims: Int, appVersion: String, proxy: ProxySpec?, log: (String) -> Unit): Credentials {
        val starter = proxy?.let { p -> TailnetStarter { _ -> p } }
        val r = EnrollFlow(OkHttpEnrollTransport(http), starter, log).run(link, { _ ->
            EnrollRequest("", "", DeviceInfoDto("JVM", "smsc-agent (simulasi)", "14", 34, appVersion),
                Capabilities(batteryOptimizationIgnored = true, tailscaleVpnActive = true),
                (0 until sims).map { SimReport(it, 10 + it, "SIM-SIMULASI-$it", registered = true, signalLevel = 4, roaming = false) })
        }, proxy = proxy)
        val c = r.credentials
        return Credentials(c.deviceId, c.deviceSecret, c.wsUrl, c.installId)
    }
}
