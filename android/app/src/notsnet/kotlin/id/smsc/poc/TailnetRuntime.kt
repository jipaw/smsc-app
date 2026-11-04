package id.smsc.poc

import android.content.Context
import id.smsc.poc.core.ProxySpec

/** Build tanpa app/libs/tsnetbridge.aar: mode tailnet ter-embed tidak tersedia (pakai aplikasi Tailscale + Always-on VPN). */
object TailnetRuntime {
    const val AVAILABLE = false
    val summary: String = "tidak tersedia di build ini"

    fun start(ctx: Context, hostname: String, authKey: String, controlServer: String): ProxySpec =
        throw IllegalStateException("APK dibangun tanpa tsnetbridge.aar (lihat android/tsnetbridge/README.md)")

    fun status(): String = "tidak tersedia di build ini"

    fun stop() {}
}
