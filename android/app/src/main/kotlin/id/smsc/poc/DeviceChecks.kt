package id.smsc.poc

import android.Manifest
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.os.BatteryManager
import android.os.Build
import android.os.PowerManager
import android.provider.Settings
import android.provider.Telephony
import id.smsc.poc.core.Battery
import id.smsc.poc.core.Capabilities
import id.smsc.poc.core.NetworkInfoDto

data class Check(val name: String, val ok: Boolean, val detail: String = "")

object DeviceChecks {
    fun granted(ctx: Context, perm: String) = ctx.checkSelfPermission(perm) == PackageManager.PERMISSION_GRANTED

    fun batteryOptimizationIgnored(ctx: Context): Boolean =
        ctx.getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(ctx.packageName)

    fun defaultSmsApp(ctx: Context): Boolean = Telephony.Sms.getDefaultSmsPackage(ctx) == ctx.packageName

    @Suppress("DEPRECATION")
    fun vpnActive(ctx: Context): Boolean {
        val cm = ctx.getSystemService(ConnectivityManager::class.java)
        return cm.allNetworks.any { cm.getNetworkCapabilities(it)?.hasTransport(NetworkCapabilities.TRANSPORT_VPN) == true }
    }

    fun networkType(ctx: Context): String {
        val cm = ctx.getSystemService(ConnectivityManager::class.java)
        val caps = cm.getNetworkCapabilities(cm.activeNetwork ?: return "none") ?: return "none"
        return when {
            caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
            else -> "none"
        }
    }

    fun battery(ctx: Context): Battery {
        val i: Intent? = ctx.registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        val level = i?.getIntExtra(BatteryManager.EXTRA_LEVEL, -1) ?: -1
        val scale = i?.getIntExtra(BatteryManager.EXTRA_SCALE, 100) ?: 100
        val status = i?.getIntExtra(BatteryManager.EXTRA_STATUS, -1) ?: -1
        val charging = status == BatteryManager.BATTERY_STATUS_CHARGING || status == BatteryManager.BATTERY_STATUS_FULL
        val temp = (i?.getIntExtra(BatteryManager.EXTRA_TEMPERATURE, 0) ?: 0) / 10.0
        return Battery(if (level < 0) 0 else level * 100 / scale, charging, temp)
    }

    fun network(ctx: Context) = NetworkInfoDto(networkType(ctx), vpnActive(ctx))

    fun capabilities(ctx: Context) = Capabilities(
        defaultSmsApp = defaultSmsApp(ctx),
        osSmsLimitUnlocked = defaultSmsApp(ctx), // hipotesis R1; dikoreksi dari hasil PoC
        batteryOptimizationIgnored = batteryOptimizationIgnored(ctx),
        tailscaleVpnActive = vpnActive(ctx),
    )

    /** Nilai pengaturan sistem yang relevan dengan batas kirim OS (hanya dibaca, bila tersedia). */
    fun osSmsLimitSettings(ctx: Context): String {
        fun read(k: String) = runCatching { Settings.Global.getString(ctx.contentResolver, k) }.getOrNull() ?: "(tidak diset)"
        return "sms_outgoing_check_max_count=${read("sms_outgoing_check_max_count")}, " +
            "interval_ms=${read("sms_outgoing_check_interval_ms")}"
    }

    fun run(ctx: Context): List<Check> {
        val out = mutableListOf<Check>()
        out += Check("Perangkat", true, "${Build.MANUFACTURER} ${Build.MODEL}, Android ${Build.VERSION.RELEASE} (API ${Build.VERSION.SDK_INT})")
        out += Check("Izin SEND_SMS", granted(ctx, Manifest.permission.SEND_SMS))
        out += Check("Izin READ_PHONE_STATE", granted(ctx, Manifest.permission.READ_PHONE_STATE))
        if (Build.VERSION.SDK_INT >= 33) out += Check("Izin notifikasi", granted(ctx, Manifest.permission.POST_NOTIFICATIONS))
        out += Check("Abaikan optimasi baterai", batteryOptimizationIgnored(ctx))
        out += Check("VPN aktif (Tailscale)", vpnActive(ctx))
        out += Check("Jaringan", networkType(ctx) != "none", networkType(ctx))
        out += Check("App SMS default", defaultSmsApp(ctx), "eksperimen R1, bukan syarat")
        out += Check("Batas OS (settings)", true, osSmsLimitSettings(ctx))
        val sims = SimReader.list(ctx)
        out += Check("SIM aktif", sims.isNotEmpty(), "${sims.size} SIM")
        return out
    }
}
