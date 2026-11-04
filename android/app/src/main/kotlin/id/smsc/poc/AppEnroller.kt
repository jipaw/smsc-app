package id.smsc.poc

import android.content.Context
import android.os.Build
import id.smsc.poc.core.DeviceCredentials
import id.smsc.poc.core.DeviceInfoDto
import id.smsc.poc.core.EnrollException
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
import java.util.concurrent.TimeUnit

/** Transport OkHttp untuk EnrollFlow (sama dengan agen JVM: id.smsc.agent.OkHttpEnrollTransport). */
class OkHttpEnrollTransport(private val base: OkHttpClient) : EnrollTransport {
    override fun postJson(url: String, json: String, proxy: ProxySpec?): Pair<Int, String> {
        val http = if (proxy != null) base.newBuilder().applyProxy(proxy).build() else base
        val req = Request.Builder().url(url).post(json.toRequestBody("application/json".toMediaType())).build()
        http.newCall(req).execute().use { return it.code to (it.body?.string() ?: "") }
    }
}

/** Enrollment dari QR/kode: EnrollFlow (:core) + tailnet ter-embed bila tersedia + simpan kredensial di Keystore. Blocking. */
object AppEnroller {
    fun enroll(ctx: Context, link: EnrollLink, log: (String) -> Unit): DeviceCredentials {
        val http = OkHttpClient.Builder().connectTimeout(20, TimeUnit.SECONDS).readTimeout(30, TimeUnit.SECONDS).build()
        val host = Prefs.tailnetHostnameOrDefault(ctx)
        val starter = if (TailnetRuntime.AVAILABLE) TailnetStarter { key -> TailnetRuntime.start(ctx, host, key, Prefs.tailnetControl(ctx)) } else null
        val r = EnrollFlow(OkHttpEnrollTransport(http), starter, log).run(link, { _ -> request(ctx) }, Prefs.installId(ctx))
        if (!CredentialStore.save(ctx, r.credentials)) throw EnrollException("kredensial tidak dapat disimpan di Keystore")
        if (r.credentials.viaTailnet) Prefs.setTailnet(ctx, true, "", Prefs.tailnetHostname(ctx), Prefs.tailnetControl(ctx))
        PocLog.i("enroll", "terdaftar ${r.credentials}")
        return r.credentials
    }

    private fun request(ctx: Context): EnrollRequest {
        val version = runCatching { ctx.packageManager.getPackageInfo(ctx.packageName, 0).versionName }.getOrNull() ?: "poc"
        val sims = SimReader.list(ctx).map { SimReport(it.slotIndex, it.subscriptionId, it.carrier, it.displayName, it.number, it.isEsim) }
        return EnrollRequest(
            token = "", installId = "",
            deviceInfo = DeviceInfoDto(Build.MANUFACTURER, Build.MODEL, Build.VERSION.RELEASE, Build.VERSION.SDK_INT, version),
            capabilities = DeviceChecks.capabilities(ctx), sims = sims,
        )
    }
}
