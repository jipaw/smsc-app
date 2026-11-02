package id.smsc.poc

import id.smsc.poc.core.AndroidSms
import id.smsc.poc.core.Ids
import id.smsc.poc.core.JobState
import id.smsc.poc.core.SimRef
import id.smsc.poc.core.SmsSend
import id.smsc.poc.core.Time

/** Uji R1/R5/R6 lokal: memasukkan N perintah ke CommandProcessor yang SAMA dengan jalur server. */
object BurstRunner {
    @Volatile var submitted = 0
        private set

    fun start(slot: Int, to: String, count: Int, minGapMs: Long, maxGapMs: Long, osGuard: Boolean, body: String): String {
        PocRuntime.applyGateCfg(GateCfg(minGapMs, maxGapMs, osGuard))
        Metrics.reset()
        submitted = 0
        val notAfter = Time.iso(System.currentTimeMillis() + 2 * 3600_000L)
        var rejected = 0
        repeat(count) { i ->
            val cmd = SmsSend(Ids.v7(), 1, SimRef(slot), to, "$body #${i + 1}", true, notAfter, null)
            val ack = PocRuntime.processor.onSmsSend(cmd)
            if (ack.accepted) submitted++ else { rejected++; PocLog.i("burst", "ditolak: ${ack.reason}") }
        }
        PocLog.i("burst", "mulai slot=$slot n=$count diterima=$submitted ditolak=$rejected gap=$minGapMs..$maxGapMs osGuard=$osGuard " +
            "(RESULT_OK=${AndroidSms.RESULT_OK})")
        PocRuntime.schedulePump(0)
        return "diterima=$submitted ditolak=$rejected"
    }

    /** Batalkan semua yang belum mulai dikirim. */
    fun cancelAll(): Int {
        var n = 0
        PocRuntime.store.all().filter { it.state == JobState.ACCEPTED }.forEach {
            if (PocRuntime.processor.onSmsCancel(it.messageId).accepted) n++
        }
        PocLog.i("burst", "dibatalkan=$n")
        return n
    }
}
