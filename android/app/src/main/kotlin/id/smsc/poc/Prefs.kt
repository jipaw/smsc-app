package id.smsc.poc

import android.content.Context
import android.content.SharedPreferences

object Prefs {
    private fun sp(ctx: Context): SharedPreferences = ctx.getSharedPreferences("poc", Context.MODE_PRIVATE)

    fun serverUrl(ctx: Context) = sp(ctx).getString("server_url", "ws://100.x.y.z:8765/ws/device") ?: ""
    fun token(ctx: Context) = sp(ctx).getString("token", "poc-secret") ?: ""
    fun wakeLock(ctx: Context) = sp(ctx).getBoolean("wake_lock", false)
    fun serviceEnabled(ctx: Context) = sp(ctx).getBoolean("service_enabled", false)

    fun setServer(ctx: Context, url: String, token: String, wakeLock: Boolean) =
        sp(ctx).edit().putString("server_url", url).putString("token", token).putBoolean("wake_lock", wakeLock).apply()

    fun setServiceEnabled(ctx: Context, on: Boolean) = sp(ctx).edit().putBoolean("service_enabled", on).apply()

    fun getString(ctx: Context, key: String, def: String) = sp(ctx).getString(key, def) ?: def
    fun putString(ctx: Context, key: String, v: String) = sp(ctx).edit().putString(key, v).apply()
}
