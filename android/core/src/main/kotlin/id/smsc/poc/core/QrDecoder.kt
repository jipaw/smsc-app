package id.smsc.poc.core

import com.google.zxing.BarcodeFormat
import com.google.zxing.BinaryBitmap
import com.google.zxing.DecodeHintType
import com.google.zxing.NotFoundException
import com.google.zxing.PlanarYUVLuminanceSource
import com.google.zxing.ReaderException
import com.google.zxing.common.HybridBinarizer
import com.google.zxing.qrcode.QRCodeReader

/**
 * Decode QR dari bidang luminance (Y) frame kamera YUV_420_888 / NV21. `rowStride` boleh > width (padding baris).
 * QR tahan rotasi, jadi orientasi sensor tidak perlu dikoreksi. Hanya area tengah (`cropFraction`) yang dibaca agar cepat.
 */
object QrDecoder {
    private val hints = mapOf(
        DecodeHintType.POSSIBLE_FORMATS to listOf(BarcodeFormat.QR_CODE),
        DecodeHintType.TRY_HARDER to true,
        DecodeHintType.CHARACTER_SET to "UTF-8",
    )

    fun decodeLuminance(y: ByteArray, width: Int, height: Int, rowStride: Int = width, cropFraction: Double = 1.0): String? {
        require(rowStride >= width && y.size >= rowStride * (height - 1) + width) { "ukuran bidang Y tidak cocok" }
        val cw = (width * cropFraction).toInt().coerceIn(1, width)
        val ch = (height * cropFraction).toInt().coerceIn(1, height)
        val src = PlanarYUVLuminanceSource(y, rowStride, height, (width - cw) / 2, (height - ch) / 2, cw, ch, false)
        for (s in listOf(src, src.invert())) { // QR terang-di-gelap (layar mode gelap)
            try {
                return QRCodeReader().decode(BinaryBitmap(HybridBinarizer(s)), hints).text
            } catch (_: NotFoundException) {
            } catch (_: ReaderException) {
            }
        }
        return null
    }
}
