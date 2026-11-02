package id.smsc.poc

import android.Manifest
import android.app.Activity
import android.app.role.RoleManager
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.telephony.SubscriptionManager
import android.view.ViewGroup
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast

/** UI PoC diprogram langsung (tanpa XML) supaya modul tidak butuh resource. */
class MainActivity : Activity() {
    private val ui = Handler(Looper.getMainLooper())
    private lateinit var checksText: TextView
    private lateinit var simText: TextView
    private lateinit var burstText: TextView
    private lateinit var connText: TextView
    private lateinit var logText: TextView
    private lateinit var etTo: EditText
    private lateinit var etCount: EditText
    private lateinit var etSlot: EditText
    private lateinit var etMin: EditText
    private lateinit var etMax: EditText
    private lateinit var cbOsGuard: CheckBox
    private lateinit var etUrl: EditText
    private lateinit var etToken: EditText
    private lateinit var cbWake: CheckBox
    private var simListener: SubscriptionManager.OnSubscriptionsChangedListener? = null
    private var tick = 0

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        PocRuntime.init(this)
        setContentView(buildUi())
        refreshAll()
    }

    override fun onResume() {
        super.onResume()
        runCatching {
            if (DeviceChecks.granted(this, Manifest.permission.READ_PHONE_STATE) && simListener == null) {
                simListener = SimReader.registerListener(this) {
                    PocLog.i("sim", "OnSubscriptionsChanged -> ${SimReader.list(this).map { "slot${it.slotIndex}/sub${it.subscriptionId}" }}")
                    ui.post { refreshSims() }
                }
            }
        }
        ui.post(loop)
    }

    override fun onPause() {
        ui.removeCallbacks(loop)
        super.onPause()
    }

    override fun onDestroy() {
        simListener?.let { runCatching { SimReader.unregisterListener(this, it) } }
        super.onDestroy()
    }

    private val loop = object : Runnable {
        override fun run() {
            tick++
            burstText.text = Metrics.summary() + "\nantrean lokal=${PocRuntime.processor.queueDepth()}"
            connText.text = PocService.client?.summary() ?: "service tidak berjalan"
            logText.text = PocLog.tail(40).joinToString("\n")
            if (tick % 5 == 0) { refreshChecks(); refreshSims() }
            ui.postDelayed(this, 1000)
        }
    }

    // ---------- UI ----------
    private fun buildUi(): ScrollView {
        val root = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(24, 24, 24, 48) }
        fun title(t: String) = TextView(this).apply { text = t; textSize = 17f; setPadding(0, 28, 0, 8); setTypeface(typeface, android.graphics.Typeface.BOLD) }
        fun body() = TextView(this).apply { textSize = 12f; typeface = android.graphics.Typeface.MONOSPACE }
        fun btn(t: String, a: () -> Unit) = Button(this).apply { text = t; setOnClickListener { a() } }
        fun edit(hint: String, v: String, num: Boolean = false) = EditText(this).apply {
            this.hint = hint; setText(v); setSingleLine()
            if (num) inputType = android.text.InputType.TYPE_CLASS_NUMBER
        }
        fun row(vararg v: android.view.View) = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            v.forEach { addView(it, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)) }
        }

        root.addView(title("SMSC PoC - skeleton R1/R2/R3/R5/R6"))
        root.addView(body().apply { text = "Lihat poc/README.md untuk prosedur uji dan lembar hasil." })

        root.addView(title("1. Prasyarat & izin (R2)"))
        checksText = body(); root.addView(checksText)
        root.addView(row(
            btn("Minta izin") { requestPerms() },
            btn("Abaikan baterai") { startActivity(Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:$packageName"))) },
        ))
        root.addView(row(
            btn("Info aplikasi") { startActivity(Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.parse("package:$packageName"))) },
            btn("Jadi SMS default") { requestSmsRole() },
        ))

        root.addView(title("2. SIM (R6)"))
        simText = body(); root.addView(simText)
        root.addView(btn("Refresh SIM") { refreshSims() })

        root.addView(title("3. Uji burst SMS (R1, R5)"))
        etTo = edit("nomor tujuan (nomor uji milik sendiri)", "")
        etCount = edit("jumlah", "30", true)
        etSlot = edit("slot SIM (0/1)", "0", true)
        etMin = edit("jeda min ms", "0", true)
        etMax = edit("jeda max ms", "0", true)
        cbOsGuard = CheckBox(this).apply { text = "Aktifkan os_sms_guard 25/30 menit" }
        listOf(etTo, row(etCount, etSlot), row(etMin, etMax), cbOsGuard).forEach { root.addView(it) }
        root.addView(row(btn("Mulai burst") { startBurst() }, btn("Batalkan sisa") { toast("dibatalkan ${BurstRunner.cancelAll()}") }))
        burstText = body(); root.addView(burstText)

        root.addView(title("4. Koneksi & standby (R3)"))
        etUrl = edit("ws://100.x.y.z:8765/ws/device", Prefs.serverUrl(this))
        etToken = edit("token", Prefs.token(this))
        cbWake = CheckBox(this).apply { text = "Partial wake lock (eksperimen pembanding)"; isChecked = Prefs.wakeLock(this@MainActivity) }
        listOf(etUrl, etToken, cbWake).forEach { root.addView(it) }
        root.addView(row(btn("Start service") { startService() }, btn("Stop service") { PocService.stop(this) }))
        connText = body(); root.addView(connText)

        root.addView(title("5. Log"))
        root.addView(body().apply { text = "Berkas: ${(getExternalFilesDir(null) ?: filesDir).absolutePath}\n(adb pull untuk poc.log, ledger.csv, events_*.csv)" })
        logText = body(); root.addView(logText)
        return ScrollView(this).apply { addView(root) }
    }

    private fun refreshChecks() {
        checksText.text = DeviceChecks.run(this).joinToString("\n") { "${if (it.ok) "[OK]" else "[ ! ]"} ${it.name}${if (it.detail.isNotBlank()) ": ${it.detail}" else ""}" }
    }

    private fun refreshSims() {
        val sims = SimReader.list(this)
        simText.text = if (sims.isEmpty()) "(tidak ada SIM terbaca / izin READ_PHONE_STATE belum diberikan)"
        else sims.joinToString("\n") { "slot=${it.slotIndex} subId=${it.subscriptionId} ${it.carrier} esim=${it.isEsim} nomor=${it.number ?: "-"}" }
    }

    private fun refreshAll() { refreshChecks(); refreshSims() }

    // ---------- aksi ----------
    private fun requestPerms() {
        val perms = mutableListOf(Manifest.permission.SEND_SMS, Manifest.permission.READ_PHONE_STATE, Manifest.permission.READ_PHONE_NUMBERS)
        if (Build.VERSION.SDK_INT >= 33) perms += Manifest.permission.POST_NOTIFICATIONS
        requestPermissions(perms.toTypedArray(), 100)
        PocLog.i("perm", "requestPermissions dipanggil (jika dialog tidak muncul/abu-abu: Info aplikasi > Izinkan setelan terbatas)")
    }

    @Deprecated("Deprecated in Java")
    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        permissions.zip(grantResults.toList()).forEach { (p, r) -> PocLog.i("perm", "${p.substringAfterLast('.')} -> ${if (r == 0) "GRANTED" else "DENIED"}") }
        refreshAll()
    }

    private fun requestSmsRole() {
        val rm = getSystemService(RoleManager::class.java)
        if (!rm.isRoleAvailable(RoleManager.ROLE_SMS)) { toast("ROLE_SMS tidak tersedia"); return }
        @Suppress("DEPRECATION")
        startActivityForResult(rm.createRequestRoleIntent(RoleManager.ROLE_SMS), 101)
    }

    @Deprecated("Deprecated in Java")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        PocLog.i("role", "ROLE_SMS result=$resultCode default=${DeviceChecks.defaultSmsApp(this)}")
        refreshChecks()
    }

    private fun startBurst() {
        val to = etTo.text.toString().trim()
        if (to.isEmpty()) { toast("isi nomor tujuan"); return }
        val min = etMin.text.toString().toLongOrNull() ?: 0L
        val max = maxOf(min, etMax.text.toString().toLongOrNull() ?: min)
        val r = BurstRunner.start(
            slot = etSlot.text.toString().toIntOrNull() ?: 0, to = to, count = etCount.text.toString().toIntOrNull() ?: 1,
            minGapMs = min, maxGapMs = max, osGuard = cbOsGuard.isChecked, body = "SMSC PoC uji",
        )
        toast(r)
    }

    private fun startService() {
        Prefs.setServer(this, etUrl.text.toString().trim(), etToken.text.toString().trim(), cbWake.isChecked)
        PocService.start(this)
    }

    private fun toast(m: String) = Toast.makeText(this, m, Toast.LENGTH_SHORT).show()
}
