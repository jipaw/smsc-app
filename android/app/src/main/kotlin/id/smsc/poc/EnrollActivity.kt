package id.smsc.poc

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.InputType
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import id.smsc.poc.core.EnrollLink

/**
 * Pendaftaran device ke server:
 * - "Pindai QR" (ScanActivity) — QR dari dashboard, opsional memuat auth key tailnet (k=) untuk tailnet ter-embed;
 * - deep link `smsc://enroll?...` (mis. dari kamera bawaan / tautan yang disalin dashboard);
 * - tempel tautan atau ketik kode manual + URL server.
 * Selalu meminta konfirmasi dengan nama host server: tautan dari luar aplikasi bisa menunjuk server milik orang lain.
 */
class EnrollActivity : Activity() {
    private lateinit var status: TextView
    private lateinit var current: TextView
    private lateinit var etLink: EditText
    private lateinit var etServer: EditText
    private val ui = Handler(Looper.getMainLooper())
    @Volatile private var running = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val root = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(32, 32, 32, 32) }
        fun text(s: String, size: Float = 14f) = TextView(this).apply { text = s; textSize = size; setPadding(0, 12, 0, 12) }
        root.addView(text("Daftarkan HP ke SMSC", 20f))
        current = text(""); root.addView(current)
        root.addView(Button(this).apply { text = "Pindai QR enrollment"; setOnClickListener { startActivityForResult(Intent(this@EnrollActivity, ScanActivity::class.java), REQ_SCAN) } })
        root.addView(text("atau tempel tautan smsc://enroll… / ketik kode manual:"))
        etLink = EditText(this).apply { hint = "smsc://enroll?s=…&t=…  atau  ABCD-1234"; inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI }
        etServer = EditText(this).apply { hint = "URL server (hanya untuk kode manual), mis. https://smsc.tailnet.ts.net"; inputType = InputType.TYPE_TEXT_VARIATION_URI }
        root.addView(etLink); root.addView(etServer)
        root.addView(Button(this).apply { text = "Daftarkan"; setOnClickListener { fromInput() } })
        status = text(""); root.addView(status)
        root.addView(Button(this).apply {
            text = "Hapus pendaftaran dari HP ini"
            setOnClickListener {
                AlertDialog.Builder(this@EnrollActivity).setMessage("Hapus kredensial device dari HP ini? Service berhenti; daftarkan ulang dengan QR baru.")
                    .setPositiveButton("Hapus") { _, _ -> PocService.stop(this@EnrollActivity); CredentialStore.clear(this@EnrollActivity); refresh() }
                    .setNegativeButton("Batal", null).show()
            }
        })
        setContentView(ScrollView(this).apply { addView(root) })
        refresh()
        handleDeepLink(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleDeepLink(intent)
    }

    private fun refresh() {
        val c = CredentialStore.load(this)
        current.text = if (c == null) "Belum terdaftar." else "Terdaftar: device …${c.deviceId.takeLast(8)} → ${c.wsUrl}${if (c.viaTailnet) " (tailnet ter-embed)" else ""}"
    }

    private fun handleDeepLink(i: Intent?) {
        val data = i?.data ?: return
        if (data.scheme != "smsc") return
        i.data = null // jangan diproses ulang saat rotasi layar
        parseAndConfirm(data.toString(), null)
    }

    @Deprecated("API platform; cukup untuk PoC tanpa AndroidX")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == REQ_SCAN && resultCode == RESULT_OK) data?.getStringExtra(ScanActivity.EXTRA_TEXT)?.let { parseAndConfirm(it, null) }
    }

    private fun fromInput() = parseAndConfirm(etLink.text.toString(), etServer.text.toString())

    private fun parseAndConfirm(raw: String, server: String?) {
        val link = try { EnrollLink.parse(raw, server) } catch (e: IllegalArgumentException) { status.text = "✗ ${e.message}"; return }
        val replace = if (CredentialStore.load(this) != null) "\n\nKredensial device yang ada di HP ini akan DIGANTI." else ""
        val tail = when {
            link.tailnetKey != null && TailnetRuntime.AVAILABLE -> "\n\nQR memuat auth key Tailscale: HP akan bergabung ke tailnet sendiri (tanpa aplikasi Tailscale)."
            link.tailnetKey != null -> "\n\nQR memuat auth key Tailscale, tetapi build ini tanpa tailnet ter-embed: pastikan aplikasi Tailscale aktif."
            else -> ""
        }
        AlertDialog.Builder(this)
            .setTitle("Daftarkan ke ${link.serverHost}?")
            .setMessage("Hanya lanjutkan bila QR berasal dari dashboard SMSC Anda.$tail$replace")
            .setPositiveButton("Daftarkan") { _, _ -> enroll(link) }
            .setNegativeButton("Batal", null)
            .show()
    }

    private fun enroll(link: EnrollLink) {
        if (running) return
        running = true
        status.text = "Mendaftarkan ke ${link.serverHost}…"
        Thread({
            val msg = try {
                val c = AppEnroller.enroll(applicationContext, link) { line -> ui.post { status.text = line } }
                // service lama (kredensial lama) dihentikan lalu dimulai ulang dengan kredensial baru. Node tailnet ikut dimatikan
                // dan dinyalakan lagi dari state tersimpan (auth key sekali pakai tidak diperlukan lagi).
                PocService.stop(this)
                Prefs.setServiceEnabled(this, true)
                PocService.start(this)
                "✓ Terdaftar sebagai device …${c.deviceId.takeLast(8)}. Service dimulai."
            } catch (e: Exception) {
                PocLog.i("enroll", "gagal: ${e.message}")
                "✗ ${e.message}"
            } finally {
                running = false
            }
            ui.post { status.text = msg; refresh() }
        }, "enroll").start()
    }

    companion object {
        private const val REQ_SCAN = 41
    }
}
