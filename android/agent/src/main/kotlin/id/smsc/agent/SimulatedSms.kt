package id.smsc.agent

import id.smsc.poc.core.AndroidSms
import id.smsc.poc.core.SendRequest
import id.smsc.poc.core.SmsGateway
import id.smsc.poc.core.SmsSendException
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.TimeUnit

/** Hitung bagian SMS seperti SmsManager.divideMessage (pendekatan): GSM-7 160/153, selain itu UCS-2 70/67. */
object SmsParts {
    private const val GSM_BASIC =
        "@£\$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"
    private const val GSM_EXT = "^{}\\[~]|€\u000c"

    fun count(body: String): Int {
        val gsm = body.all { it in GSM_BASIC || it in GSM_EXT }
        if (gsm) {
            val septets = body.sumOf { if (it in GSM_EXT) 2L else 1L }.toInt()
            return if (septets <= 160) 1 else (septets + 152) / 153
        }
        val units = body.length // UTF-16 code unit
        return if (units <= 70) 1 else (units + 66) / 67
    }
}

/**
 * Perilaku simulasi (dipilih dari akhiran nomor tujuan agar dapat diuji dari server/dashboard):
 * - `...0000`: kegagalan sinkron saat memanggil SmsManager (generic_failure)
 * - `...0001`: bagian pertama terkirim lalu bagian ke-2 gagal (no_service) → gagal parsial pada pesan multi-bagian
 * - `...0002`: terkirim tetapi laporan delivery gagal permanen (delivery_failed)
 * - `...0003`: tidak ada callback sama sekali (job macet di SENDING; server akan menanyakan lewat sms.status)
 * - lainnya: setiap bagian terkirim setelah [latencyMs], terdeliver setelah [latencyMs] berikutnya.
 */
class SimulatedSmsGateway(
    private val sims: Int,
    private val latencyMs: Long,
    private val exec: ScheduledExecutorService,
    private val log: (String) -> Unit,
) : SmsGateway {
    /** Callback ke CommandProcessor; diset setelah processor dibuat. */
    lateinit var onPartSent: (messageId: String, attempt: Int, part: Int, result: Int) -> Unit
    lateinit var onPartDelivered: (messageId: String, attempt: Int, part: Int, tpStatus: Int) -> Unit

    @Volatile var sentCount = 0
        private set

    override fun hasSendPermission() = true
    override fun simAvailable(slotIndex: Int) = slotIndex in 0 until sims
    override fun partCount(body: String) = SmsParts.count(body)

    override fun send(request: SendRequest) {
        val to = request.to
        log("SmsManager.send(simulasi) msg=${request.messageId.takeLast(8)} slot=${request.slotIndex} parts=${request.partsTotal} to=${mask(to)}")
        if (to.endsWith("0000")) throw SmsSendException("generic_failure", "simulasi kegagalan sinkron", AndroidSms.ERROR_GENERIC_FAILURE)
        sentCount++
        if (to.endsWith("0003")) return
        for (i in 0 until request.partsTotal) {
            val result = if (to.endsWith("0001") && i == 1) AndroidSms.ERROR_NO_SERVICE else AndroidSms.RESULT_OK
            exec.schedule({ onPartSent(request.messageId, request.attempt, i, result) }, latencyMs * (i + 1), TimeUnit.MILLISECONDS)
            if (request.deliveryReport && result == AndroidSms.RESULT_OK) {
                val tp = if (to.endsWith("0002")) 0x40 else 0x00
                exec.schedule({ onPartDelivered(request.messageId, request.attempt, i, tp) }, latencyMs * (i + 2), TimeUnit.MILLISECONDS)
            }
        }
    }

    private fun mask(n: String) = if (n.length > 8) n.take(5) + "****" + n.takeLast(4) else n
}
