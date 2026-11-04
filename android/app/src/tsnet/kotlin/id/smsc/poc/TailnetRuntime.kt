package id.smsc.poc

import android.content.Context
import id.smsc.poc.core.ProxySpec
import java.io.File

/**
 * Node Tailscale ter-embed (AAR gomobile dari android/tsnetbridge). Dipakai bila app/libs/tsnetbridge.aar ada saat build.
 * State node (kunci) di noBackupFilesDir: privat aplikasi dan tidak ikut cadangan cloud.
 */
object TailnetRuntime {
    const val AVAILABLE = true
    private var bridge: tsnetbridge.Bridge? = null
    /** Ringkasan terakhir untuk UI (tidak memblokir thread UI). */
    @Volatile var summary: String = "belum berjalan"
        private set

    /** Menyalakan node dan menunggu tersambung (blocking; panggil dari thread latar). authKey hanya untuk login pertama. */
    @Synchronized
    fun start(ctx: Context, hostname: String, authKey: String, controlServer: String): ProxySpec {
        val b = bridge ?: tsnetbridge.Tsnetbridge.newBridge(File(ctx.noBackupFilesDir, "tailnet").absolutePath, hostname).also {
            if (controlServer.isNotBlank()) it.setControlServer(controlServer)
            it.setLogger { line -> PocLog.i("tailnet", line) }
            bridge = it
        }
        summary = "menyambung..."
        val p = try { b.start(authKey, 90_000L) } catch (e: Exception) { summary = "gagal: ${e.message}"; throw e }
        summary = b.status()
        return ProxySpec(ProxySpec.Type.SOCKS5, p.host, p.port.toInt(), p.username, p.password)
    }

    /** Status lengkap dari node (memanggil LocalAPI, jangan dari thread UI). */
    @Synchronized fun status(): String = bridge?.status() ?: "tailnet ter-embed belum berjalan"

    @Synchronized fun stop() {
        runCatching { bridge?.close() }
        bridge = null
        summary = "berhenti"
    }
}
