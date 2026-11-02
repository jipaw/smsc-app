package id.smsc.poc.core

import kotlin.random.Random

fun interface TimeSource { fun nowMs(): Long }

/** Jam dinding (untuk timestamp event). */
object WallTime : TimeSource { override fun nowMs() = System.currentTimeMillis() }

/** Jam monotonik (untuk guardrail; kebal terhadap perubahan jam sistem). */
object MonotonicTime : TimeSource { override fun nowMs() = System.nanoTime() / 1_000_000 }

/** Konstanta hasil Android (diverifikasi terhadap android.telephony.SmsManager oleh modul :typecheck). */
object AndroidSms {
    const val RESULT_OK = -1 // Activity.RESULT_OK
    const val ERROR_GENERIC_FAILURE = 1
    const val ERROR_RADIO_OFF = 2
    const val ERROR_NULL_PDU = 3
    const val ERROR_NO_SERVICE = 4
    const val ERROR_LIMIT_EXCEEDED = 5
    const val ERROR_FDN_CHECK_FAILURE = 6
    const val ERROR_SHORT_CODE_NOT_ALLOWED = 7
    const val ERROR_SHORT_CODE_NEVER_ALLOWED = 8
    const val ERROR_RADIO_NOT_AVAILABLE = 9
    const val ERROR_NETWORK_REJECT = 10
    const val ERROR_INVALID_ARGUMENTS = 11
    const val ERROR_INVALID_STATE = 12
    const val ERROR_NO_MEMORY = 13
    const val ERROR_INVALID_SMS_FORMAT = 14
}

data class MappedError(val errorCode: String, val retryableHint: Boolean)

/** Pemetaan hasil SmsManager -> error_code kontrak (device-ws-protocol.md §3.6). */
object ErrorMapper {
    fun map(resultCode: Int): MappedError = when (resultCode) {
        AndroidSms.ERROR_GENERIC_FAILURE -> MappedError("generic_failure", true)
        AndroidSms.ERROR_RADIO_OFF, AndroidSms.ERROR_RADIO_NOT_AVAILABLE -> MappedError("radio_off", true)
        AndroidSms.ERROR_NULL_PDU -> MappedError("null_pdu", false)
        AndroidSms.ERROR_NO_SERVICE -> MappedError("no_service", true)
        AndroidSms.ERROR_LIMIT_EXCEEDED -> MappedError("os_rate_limited", true)
        AndroidSms.ERROR_SHORT_CODE_NOT_ALLOWED, AndroidSms.ERROR_SHORT_CODE_NEVER_ALLOWED ->
            MappedError("short_code_blocked", false)
        AndroidSms.ERROR_FDN_CHECK_FAILURE, AndroidSms.ERROR_INVALID_ARGUMENTS, AndroidSms.ERROR_INVALID_SMS_FORMAT ->
            MappedError("invalid_destination", false)
        else -> MappedError("generic_failure", true)
    }
}

enum class DeliveryOutcome { DELIVERED, PENDING, FAILED }

/** TP-Status (3GPP TS 23.040 §9.2.3.15) dari SmsMessage.getStatus(). */
object DeliveryStatus {
    fun classify(tpStatus: Int): DeliveryOutcome = when (tpStatus) {
        in 0x00..0x1F -> DeliveryOutcome.DELIVERED      // diterima / diteruskan / diganti
        in 0x20..0x3F -> DeliveryOutcome.PENDING        // sementara gagal, SC masih mencoba
        else -> DeliveryOutcome.FAILED                  // permanen gagal / SC berhenti mencoba
    }
}

data class OsGuard(val maxPerWindow: Int, val windowSec: Int)

/**
 * Guardrail per slot SIM: jeda acak [minGapMs, maxGapMs], batas per menit, dan batas OS (mis. 25/30 mnt).
 * Penundaan di sini BUKAN kegagalan (protocol D6).
 */
class SlotGate(
    private val time: TimeSource,
    var minGapMs: Long = 0,
    var maxGapMs: Long = minGapMs,
    var maxPerMinute: Int? = null,
    var osGuard: OsGuard? = null,
    private val random: Random = Random.Default,
) {
    private val history = ArrayDeque<Long>()
    private var nextAllowedAt: Long? = null

    /** 0 bila boleh kirim sekarang, selain itu jumlah ms yang harus ditunggu. */
    @Synchronized
    fun waitMs(): Long {
        val now = time.nowMs()
        prune(now)
        var wait = nextAllowedAt?.let { maxOf(0L, it - now) } ?: 0L
        maxPerMinute?.let { wait = maxOf(wait, windowWait(now, 60_000L, it)) }
        osGuard?.let { wait = maxOf(wait, windowWait(now, it.windowSec * 1000L, it.maxPerWindow)) }
        return wait
    }

    @Synchronized
    fun record() {
        val now = time.nowMs()
        history.addLast(now)
        val gap = if (maxGapMs > minGapMs) random.nextLong(minGapMs, maxGapMs + 1) else minGapMs
        nextAllowedAt = now + gap
    }

    private fun windowWait(now: Long, windowMs: Long, max: Int): Long {
        val inWindow = history.filter { it > now - windowMs }
        if (inWindow.size < max) return 0L
        return inWindow[inWindow.size - max] + windowMs - now
    }

    private fun prune(now: Long) {
        val keep = maxOf(60_000L, (osGuard?.windowSec ?: 0) * 1000L)
        while (history.isNotEmpty() && history.first() <= now - keep) history.removeFirst()
    }
}

/** Backoff eksponensial dengan full jitter (protocol §4). */
class Backoff(
    private val minMs: Long = 1_000,
    private val maxMs: Long = 60_000,
    private val random: Random = Random.Default,
) {
    private var attempt = 0
    fun next(): Long {
        val cap = minOf(maxMs, minMs shl minOf(attempt, 20))
        attempt++
        return random.nextLong(0, cap + 1)
    }
    fun reset() { attempt = 0 }
}
