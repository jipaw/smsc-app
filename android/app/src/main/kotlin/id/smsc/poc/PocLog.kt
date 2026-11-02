package id.smsc.poc

import android.util.Log
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/** Log ke logcat + berkas poc.log + ring buffer untuk UI. Tidak pernah mencatat isi SMS. */
object PocLog {
    private val ring = ArrayDeque<String>()
    private var dir: File? = null
    private val fmt = SimpleDateFormat("MM-dd HH:mm:ss.SSS", Locale.US)

    @Synchronized fun init(dir: File) { this.dir = dir; dir.mkdirs() }

    @Synchronized fun i(tag: String, msg: String) {
        val line = "${fmt.format(Date())} [$tag] $msg"
        ring.addLast(line)
        if (ring.size > 400) ring.removeFirst()
        Log.i("SMSC-POC", line)
        runCatching { dir?.let { File(it, "poc.log").appendText(line + "\n") } }
    }

    @Synchronized fun tail(n: Int): List<String> = ring.toList().takeLast(n)

    @Synchronized fun file(name: String): File? = dir?.let { File(it, name) }

    /** Tambah baris CSV; tulis header bila berkas baru. */
    @Synchronized fun csv(name: String, header: String, row: String) {
        val f = file(name) ?: return
        runCatching {
            if (!f.exists()) f.appendText(header + "\n")
            f.appendText(row + "\n")
        }
    }
}
