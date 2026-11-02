package id.smsc.poc

import android.content.Context
import id.smsc.poc.core.CommandProcessor
import id.smsc.poc.core.MonotonicTime
import id.smsc.poc.core.Outbox
import id.smsc.poc.core.OsGuard
import id.smsc.poc.core.SlotGate
import id.smsc.poc.core.SmsEvent
import id.smsc.poc.core.WallTime
import java.io.File
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit

data class GateCfg(val minGapMs: Long = 0, val maxGapMs: Long = 0, val osGuard: Boolean = false)

/** Singleton proses: dipakai Activity, Service, dan Receiver. */
object PocRuntime {
    @Volatile private var inited = false
    lateinit var appCtx: Context
        private set
    lateinit var store: FileJobStore
        private set
    lateinit var processor: CommandProcessor
        private set
    val outbox = Outbox()
    val listeners = CopyOnWriteArrayList<(SmsEvent) -> Unit>()

    private val gates = ConcurrentHashMap<Int, SlotGate>()
    @Volatile private var gateCfg = GateCfg()
    private val exec = Executors.newSingleThreadScheduledExecutor { r -> Thread(r, "poc-pump").apply { isDaemon = true } }
    private var pumpFuture: ScheduledFuture<*>? = null
    private val sessionTag = System.currentTimeMillis().toString()

    @Synchronized
    fun init(ctx: Context) {
        if (inited) return
        appCtx = ctx.applicationContext
        val dir = appCtx.getExternalFilesDir(null) ?: appCtx.filesDir
        PocLog.init(dir)
        store = FileJobStore(File(appCtx.filesDir, "jobs.json"))
        processor = CommandProcessor(store, AndroidSmsGateway(appCtx), WallTime, ::gateFor, ::onEvent)
        inited = true
        PocLog.i("runtime", "init pid=${android.os.Process.myPid()} dir=$dir jobs=${store.all().size}")
    }

    private fun gateFor(slot: Int): SlotGate = gates.getOrPut(slot) {
        val c = gateCfg
        SlotGate(MonotonicTime, c.minGapMs, c.maxGapMs, null, if (c.osGuard) OsGuard(25, 1800) else null)
    }

    /** Terapkan konfigurasi ke gate yang sudah ada maupun yang baru. */
    fun applyGateCfg(cfg: GateCfg) {
        gateCfg = cfg
        gates.values.forEach {
            it.minGapMs = cfg.minGapMs
            it.maxGapMs = cfg.maxGapMs
            it.osGuard = if (cfg.osGuard) OsGuard(25, 1800) else null
        }
    }

    private fun onEvent(ev: SmsEvent) {
        outbox.add(ev)
        Metrics.onEvent(ev)
        PocLog.csv(
            "events_$sessionTag.csv",
            "ts_ms,at,message_id,attempt,state,part_index,parts_total,error_code,android_result_code",
            "${System.currentTimeMillis()},${ev.at},${ev.messageId},${ev.attempt},${ev.state},${ev.partIndex ?: ""},${ev.partsTotal},${ev.errorCode ?: ""},${ev.androidResultCode ?: ""}",
        )
        PocLog.i("evt", "${ev.messageId.take(8)} ${ev.state}${ev.partIndex?.let { " part=$it" } ?: ""}${ev.errorCode?.let { " err=$it" } ?: ""}")
        listeners.forEach { runCatching { it(ev) } }
    }

    /** Jadwalkan pump() (menggantikan jadwal sebelumnya). */
    @Synchronized
    fun schedulePump(delayMs: Long) {
        pumpFuture?.cancel(false)
        pumpFuture = exec.schedule({ runPump() }, delayMs, TimeUnit.MILLISECONDS)
    }

    private fun runPump() {
        val next = try { processor.pump() } catch (t: Throwable) { PocLog.i("pump", "error $t"); null }
        if (next != null) schedulePump(next)
    }
}
