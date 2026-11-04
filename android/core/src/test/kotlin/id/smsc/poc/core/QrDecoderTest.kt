package id.smsc.poc.core

import com.google.zxing.BarcodeFormat
import com.google.zxing.EncodeHintType
import com.google.zxing.qrcode.QRCodeWriter
import com.google.zxing.qrcode.decoder.ErrorCorrectionLevel
import kotlin.random.Random
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

class QrDecoderTest {
    private val link = "smsc://enroll?s=https%3A%2F%2Fsmsc.tail1234.ts.net&t=" + "a".repeat(43) + "&k=tskey-auth-kAbCdEf1CNTRL-" + "Z".repeat(30)

    /** Frame kamera sintetis: QR (skala modul `scale`) di dalam frame width×height, rowStride berpadding, opsional rotasi 90°/terbalik/noise. */
    private fun frame(text: String, width: Int, height: Int, rowStride: Int, scale: Int, rotate: Boolean = false, invert: Boolean = false, noise: Int = 0): ByteArray {
        val m = QRCodeWriter().encode(text, BarcodeFormat.QR_CODE, 0, 0, mapOf(EncodeHintType.ERROR_CORRECTION to ErrorCorrectionLevel.M, EncodeHintType.MARGIN to 4))
        val size = m.width * scale
        val y = ByteArray(rowStride * height) { 130.toByte() } // latar abu-abu (meja/layar)
        val ox = (width - size) / 2
        val oy = (height - size) / 2
        val rnd = Random(7)
        for (py in 0 until size) for (px in 0 until size) {
            val mx = (if (rotate) py else px) / scale
            val my = (if (rotate) size - 1 - px else py) / scale
            var v = if (m.get(mx, my)) 25 else 230
            if (invert) v = 255 - v
            v = (v + if (noise > 0) rnd.nextInt(-noise, noise + 1) else 0).coerceIn(0, 255)
            y[(oy + py) * rowStride + ox + px] = v.toByte()
        }
        return y
    }

    @Test fun decodesCameraFrames() {
        assertEquals(link, QrDecoder.decodeLuminance(frame(link, 1280, 720, 1280, 5), 1280, 720))
        assertEquals(link, QrDecoder.decodeLuminance(frame(link, 1280, 720, 1344, 5), 1280, 720, rowStride = 1344), "padding baris")
        assertEquals(link, QrDecoder.decodeLuminance(frame(link, 1280, 720, 1280, 5, rotate = true), 1280, 720), "sensor terputar 90°")
        assertEquals(link, QrDecoder.decodeLuminance(frame(link, 1280, 720, 1280, 5, invert = true), 1280, 720), "mode gelap (terbalik)")
        assertEquals(link, QrDecoder.decodeLuminance(frame(link, 1280, 720, 1280, 5, noise = 40), 1280, 720), "noise sensor")
        assertEquals(link, QrDecoder.decodeLuminance(frame(link, 1280, 720, 1280, 5), 1280, 720, cropFraction = 0.8), "crop tengah")
        assertEquals(link, EnrollLink.parse(QrDecoder.decodeLuminance(frame(link, 1280, 720, 1280, 5), 1280, 720)!!, null).let {
            "smsc://enroll?s=https%3A%2F%2Fsmsc.tail1234.ts.net&t=${it.token}&k=${it.tailnetKey}"
        })
    }

    @Test fun emptyFrameReturnsNull() {
        assertNull(QrDecoder.decodeLuminance(ByteArray(640 * 480) { 128.toByte() }, 640, 480))
    }
}
