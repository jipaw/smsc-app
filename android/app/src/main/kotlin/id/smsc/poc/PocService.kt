package id.smsc.poc

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.os.Build
import android.os.IBinder
import android.os.PowerManager

/** Foreground service (specialUse) yang menjaga koneksi WebSocket. Eksperimen R3. */
class PocService : Service() {
    private var wakeLock: PowerManager.WakeLock? = null
    private var netCb: ConnectivityManager.NetworkCallback? = null
    @Volatile private var tailnetStarting = false

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        PocRuntime.init(this)
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CHANNEL, "SMSC PoC", NotificationManager.IMPORTANCE_LOW))
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            Prefs.setServiceEnabled(this, false)
            teardown("stop manual")
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
            return START_NOT_STICKY
        }
        startForegroundCompat(buildNotification("memulai..."))
        PocLog.i("svc", "onStartCommand restart=${intent == null} flags=$flags")
        if (client == null) {
            val creds = CredentialStore.load(this) // hasil enrollment QR; bila tidak ada, setelan manual PoC
            val url = creds?.wsUrl ?: Prefs.serverUrl(this)
            if (url.isBlank() || url.contains("100.x.y.z")) {
                PocLog.i("svc", "URL server belum diisi, service berhenti")
                stopSelf(); return START_NOT_STICKY
            }
            Prefs.setServiceEnabled(this, true)
            if (Prefs.wakeLock(this)) {
                wakeLock = getSystemService(PowerManager::class.java)
                    .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "smsc:poc").apply { setReferenceCounted(false); acquire() }
                PocLog.i("svc", "partial wake lock DIAKTIFKAN (eksperimen)")
            }
            val token = creds?.deviceSecret ?: Prefs.token(this)
            if (Prefs.tailnetEnabled(this)) startViaTailnet(url, token) else client = GatewayClient(applicationContext, url, token).also { it.start() }
            registerNetworkCallback()
        }
        return START_STICKY
    }

    /** Tailnet ter-embed: nyalakan node tsnet (blocking, di thread latar), lalu sambungkan WebSocket lewat proxy SOCKS5-nya. */
    private fun startViaTailnet(url: String, token: String) {
        if (tailnetStarting) return
        tailnetStarting = true
        updateNotification(this, "menyambung ke tailnet...")
        Thread({
            try {
                val host = Prefs.tailnetHostnameOrDefault(this)
                val spec = TailnetRuntime.start(applicationContext, host, Prefs.tailnetAuthKey(this), Prefs.tailnetControl(this))
                Prefs.clearTailnetAuthKey(this) // kunci node sudah tersimpan; auth key tidak diperlukan lagi
                PocLog.i("tailnet", "tersambung sebagai $host, ${TailnetRuntime.status()}")
                if (client == null && Prefs.serviceEnabled(this)) client = GatewayClient(applicationContext, url, token, spec).also { it.start() }
            } catch (e: Exception) {
                PocLog.i("tailnet", "gagal menyalakan tailnet ter-embed: ${e.message}")
                updateNotification(this, "tailnet gagal: ${e.message}")
            } finally {
                tailnetStarting = false
            }
        }, "tailnet-start").start()
    }

    override fun onDestroy() {
        PocLog.i("svc", "onDestroy (dimatikan sistem atau stop)")
        teardown("onDestroy")
        super.onDestroy()
    }

    private fun teardown(why: String) {
        PocLog.i("svc", "teardown: $why")
        netCb?.let { runCatching { getSystemService(ConnectivityManager::class.java).unregisterNetworkCallback(it) } }
        netCb = null
        client?.stop(); client = null
        TailnetRuntime.stop()
        wakeLock?.let { if (it.isHeld) it.release() }; wakeLock = null
    }

    private fun registerNetworkCallback() {
        val cm = getSystemService(ConnectivityManager::class.java)
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                val vpn = cm.getNetworkCapabilities(network)?.hasTransport(NetworkCapabilities.TRANSPORT_VPN) == true
                PocLog.i("net", "available vpn=$vpn")
                client?.connectNow("network_available vpn=$vpn")
            }
            override fun onLost(network: Network) { PocLog.i("net", "lost") }
        }
        cm.registerDefaultNetworkCallback(cb)
        netCb = cb
    }

    private fun startForegroundCompat(n: Notification) {
        if (Build.VERSION.SDK_INT >= 34) startForeground(NOTIF_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        else startForeground(NOTIF_ID, n)
    }

    private fun buildNotification(text: String): Notification =
        Notification.Builder(this, CHANNEL)
            .setContentTitle("SMSC PoC")
            .setContentText(text)
            .setSmallIcon(android.R.drawable.stat_notify_sync)
            .setOngoing(true)
            .build()

    companion object {
        private const val CHANNEL = "smsc_poc"
        private const val NOTIF_ID = 1
        const val ACTION_STOP = "id.smsc.poc.STOP"
        @Volatile var client: GatewayClient? = null

        fun start(ctx: Context) {
            ctx.startForegroundService(Intent(ctx, PocService::class.java))
        }

        fun stop(ctx: Context) {
            ctx.startService(Intent(ctx, PocService::class.java).setAction(ACTION_STOP))
        }

        fun updateNotification(ctx: Context, text: String) {
            val n = Notification.Builder(ctx, CHANNEL)
                .setContentTitle("SMSC PoC").setContentText(text)
                .setSmallIcon(android.R.drawable.stat_notify_sync).setOngoing(true).build()
            runCatching { ctx.getSystemService(NotificationManager::class.java).notify(NOTIF_ID, n) }
        }
    }
}
