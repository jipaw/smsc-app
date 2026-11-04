package id.smsc.agent

import id.smsc.poc.core.QrDecoder
import java.io.File
import javax.imageio.ImageIO

/** Decode QR dari berkas gambar (PNG/JPEG) dengan QrDecoder yang sama dengan ScanActivity di aplikasi (bidang luminance). */
object QrImage {
    fun decode(file: File): String? {
        val img = ImageIO.read(file) ?: throw IllegalArgumentException("bukan gambar: $file")
        val w = img.width
        val h = img.height
        val y = ByteArray(w * h)
        for (py in 0 until h) for (px in 0 until w) {
            val rgb = img.getRGB(px, py)
            val a = rgb ushr 24 and 0xff
            val r = rgb shr 16 and 0xff
            val g = rgb shr 8 and 0xff
            val b = rgb and 0xff
            // luma BT.601; piksel transparan dianggap putih (latar halaman)
            val l = (299 * r + 587 * g + 114 * b) / 1000
            y[py * w + px] = (if (a < 128) 255 else l).toByte()
        }
        return QrDecoder.decodeLuminance(y, w, h)
    }
}
