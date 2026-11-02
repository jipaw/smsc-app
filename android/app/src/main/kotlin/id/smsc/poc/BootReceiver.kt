package id.smsc.poc

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** Memulai ulang service setelah reboot bila sebelumnya aktif (uji R3 poin reboot). */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        PocRuntime.init(context)
        val enabled = Prefs.serviceEnabled(context)
        PocLog.i("boot", "BOOT_COMPLETED serviceEnabled=$enabled")
        if (enabled) runCatching { PocService.start(context) }.onFailure { PocLog.i("boot", "gagal start FGS: $it") }
    }
}
