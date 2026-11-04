package id.smsc.poc.core

import kotlinx.serialization.Serializable

@Serializable
enum class JobState { ACCEPTED, SENDING, SENT, DELIVERED, FAILED }

@Serializable
data class SmsJob(
    val messageId: String,
    val attempt: Int,
    val state: JobState,
    val slotIndex: Int,
    val partsTotal: Int,
    val deliveryReport: Boolean,
    val sentParts: Set<Int> = emptySet(),
    val deliveredParts: Set<Int> = emptySet(),
    val errorCode: String? = null,
    val updatedAtMs: Long = 0,
) {
    val terminal: Boolean get() = state == JobState.DELIVERED || state == JobState.FAILED
}

interface JobStore {
    fun get(messageId: String): SmsJob?
    fun put(job: SmsJob)
    fun all(): List<SmsJob>
}

class InMemoryJobStore : JobStore {
    private val map = LinkedHashMap<String, SmsJob>()
    @Synchronized override fun get(messageId: String) = map[messageId]
    @Synchronized override fun put(job: SmsJob) { map[job.messageId] = job }
    @Synchronized override fun all() = map.values.toList()
}

data class SendRequest(
    val messageId: String,
    val attempt: Int,
    val slotIndex: Int,
    val to: String,
    val body: String,
    val partsTotal: Int,
    val deliveryReport: Boolean,
)

class SmsSendException(val errorCode: String, message: String, val androidCode: Int? = null) : Exception(message)

/** Batas ke platform Android. Implementasi nyata: AndroidSmsGateway di :app. */
interface SmsGateway {
    fun hasSendPermission(): Boolean
    fun simAvailable(slotIndex: Int): Boolean
    fun partCount(body: String): Int
    /** Panggil SmsManager. Lempar [SmsSendException] untuk kegagalan sinkron. */
    fun send(request: SendRequest)
}

/** Antrean event keluar (outbox). Event dihapus hanya setelah event.ack (protocol D4). */
class Outbox(private val max: Int = 10_000) {
    private val events = LinkedHashMap<String, SmsEvent>()
    private val informational = setOf("sending", "part_sent", "part_delivered")

    @Synchronized
    fun add(e: SmsEvent) {
        if (events.size >= max) {
            val victim = events.values.firstOrNull { it.state in informational } ?: events.values.first()
            events.remove(victim.eventId)
        }
        events[e.eventId] = e
    }
    @Synchronized fun pending(): List<SmsEvent> = events.values.toList()
    @Synchronized fun ack(ids: Collection<String>) { ids.forEach { events.remove(it) } }
    @Synchronized fun size() = events.size
}

/**
 * Inti perilaku device (protocol §4, spec 03 §5): validasi -> dedup -> persist -> antrean per slot ->
 * guardrail -> SmsManager, serta agregasi hasil per-part menjadi event. Murni JVM supaya dapat diuji.
 */
class CommandProcessor(
    private val store: JobStore,
    private val gateway: SmsGateway,
    private val wallClock: TimeSource,
    private val gateFactory: (slotIndex: Int) -> SlotGate,
    private val emit: (SmsEvent) -> Unit,
    private val newId: () -> String = { Ids.v7() },
) {
    private val lock = Any()
    private val queues = LinkedHashMap<Int, ArrayDeque<SmsSend>>()
    private val gates = HashMap<Int, SlotGate>()

    fun onSmsSend(cmd: SmsSend): CommandAck = synchronized(lock) {
        val notAfter = Time.parseMs(cmd.notAfter)
        if (notAfter != null && wallClock.nowMs() > notAfter) return CommandAck(false, "expired")
        store.get(cmd.messageId)?.let { existing ->
            if (existing.attempt >= cmd.attempt) {
                val busy = existing.state == JobState.ACCEPTED || existing.state == JobState.SENDING
                return CommandAck(false, if (busy) "already_sending" else "duplicate")
            }
        }
        if (!gateway.hasSendPermission()) return CommandAck(false, "permission_denied")
        if (!gateway.simAvailable(cmd.sim.slotIndex)) return CommandAck(false, "sim_unavailable")
        val parts = gateway.partCount(cmd.body)
        // persist-before-ack (D1)
        store.put(
            SmsJob(
                cmd.messageId, cmd.attempt, JobState.ACCEPTED, cmd.sim.slotIndex, parts,
                cmd.deliveryReport, updatedAtMs = wallClock.nowMs(),
            ),
        )
        queues.getOrPut(cmd.sim.slotIndex) { ArrayDeque() }.addLast(cmd)
        CommandAck(true, null)
    }

    fun onSmsCancel(messageId: String): CommandAck = synchronized(lock) {
        val job = store.get(messageId) ?: return CommandAck(true, null)
        if (job.state != JobState.ACCEPTED) return CommandAck(false, "already_sending")
        queues[job.slotIndex]?.removeAll { it.messageId == messageId }
        store.put(job.copy(state = JobState.FAILED, errorCode = "cancelled", updatedAtMs = wallClock.nowMs()))
        CommandAck(true, null)
    }

    /**
     * Jawab sms.status dari penyimpanan lokal, tanpa efek samping (tidak mengirim ulang, tidak membuat event).
     * Job tidak ada -> state "unknown". error_code dibatasi ke enum kontrak ("cancelled" lokal dilaporkan generic_failure).
     */
    fun onSmsStatus(q: SmsStatus): SmsState = synchronized(lock) {
        val job = store.get(q.messageId) ?: return SmsState(q.messageId, q.attempt, "unknown")
        val code = if (job.state == JobState.FAILED) (job.errorCode?.takeIf { it in CONTRACT_ERROR_CODES } ?: "generic_failure") else null
        SmsState(job.messageId, job.attempt, job.state.name.lowercase(), job.partsTotal.takeIf { it > 0 },
            job.sentParts.size, job.deliveredParts.size, code)
    }

    /**
     * Kirim item yang sudah diizinkan guardrail. Kembalikan ms sampai perlu dipanggil lagi,
     * atau null bila tidak ada antrean.
     */
    fun pump(): Long? = synchronized(lock) {
        var next: Long? = null
        for ((slot, queue) in queues) {
            while (queue.isNotEmpty()) {
                val cmd = queue.first()
                val job = store.get(cmd.messageId)
                if (job == null || job.state != JobState.ACCEPTED) { queue.removeFirst(); continue }
                val notAfter = Time.parseMs(cmd.notAfter)
                if (notAfter != null && wallClock.nowMs() > notAfter) {
                    queue.removeFirst(); fail(job, "expired", null); continue
                }
                val gate = gates.getOrPut(slot) { gateFactory(slot) }
                cmd.minGapMs?.let { if (it > gate.minGapMs) { gate.minGapMs = it; if (gate.maxGapMs < it) gate.maxGapMs = it } }
                val wait = gate.waitMs()
                if (wait > 0) { next = minOf(next ?: wait, wait); break }
                queue.removeFirst()
                sendNow(cmd, job, gate)
                if (queue.isNotEmpty()) {   // minta dipanggil lagi hanya bila masih ada antrean di slot ini
                    val after = gate.waitMs()
                    if (after > 0) { next = minOf(next ?: after, after); break }
                }
            }
        }
        next
    }

    private fun sendNow(cmd: SmsSend, accepted: SmsJob, gate: SlotGate) {
        // persist-before-act (D1): status SENDING tersimpan sebelum SmsManager dipanggil
        val sending = accepted.copy(state = JobState.SENDING, updatedAtMs = wallClock.nowMs())
        store.put(sending)
        emit(event(sending, "sending"))
        try {
            gateway.send(
                SendRequest(cmd.messageId, cmd.attempt, cmd.sim.slotIndex, cmd.to, cmd.body, sending.partsTotal, cmd.deliveryReport),
            )
            gate.record()
        } catch (e: SmsSendException) {
            gate.record()
            fail(sending, e.errorCode, e.androidCode)
        } catch (e: RuntimeException) {
            gate.record()
            fail(sending, "generic_failure", null)
        }
    }

    /** Hasil sentIntent untuk satu part. */
    fun onPartSent(messageId: String, attempt: Int, partIndex: Int, resultCode: Int) = synchronized(lock) {
        val job = store.get(messageId)?.takeIf { it.attempt == attempt } ?: return@synchronized
        if (partIndex !in 0 until job.partsTotal) return@synchronized      // indeks di luar rentang (intent usang/rusak): abaikan
        if (job.terminal) {
            // Bagian yang sukses terkirim SETELAH job gagal (modem tetap mengirim sisa bagian) dicatat diam-diam agar hitungan yang
            // dilaporkan sms.state akurat; tidak membuat event dan tidak mengubah state (FAILED tetap FAILED).
            if (job.state == JobState.FAILED && resultCode == AndroidSms.RESULT_OK && partIndex !in job.sentParts) {
                store.put(job.copy(sentParts = job.sentParts + partIndex, updatedAtMs = wallClock.nowMs()))
            }
            return@synchronized
        }
        if (resultCode == AndroidSms.RESULT_OK) {
            if (partIndex in job.sentParts) return@synchronized
            val updated = job.copy(sentParts = job.sentParts + partIndex, updatedAtMs = wallClock.nowMs())
            store.put(updated)
            emit(event(updated, "part_sent", partIndex = partIndex, android = resultCode))
            settle(updated, resultCode)
        } else if (job.state == JobState.SENDING) {
            val m = ErrorMapper.map(resultCode)
            fail(job, m.errorCode, resultCode)
        }
    }

    /** Laporan delivery untuk satu part (tpStatus dari SmsMessage.getStatus()). */
    fun onPartDelivered(messageId: String, attempt: Int, partIndex: Int, tpStatus: Int) = synchronized(lock) {
        val job = store.get(messageId)?.takeIf { it.attempt == attempt } ?: return@synchronized
        if (job.terminal || !job.deliveryReport) return@synchronized
        when (DeliveryStatus.classify(tpStatus)) {
            DeliveryOutcome.PENDING -> Unit
            DeliveryOutcome.FAILED -> fail(job, "delivery_failed", tpStatus)
            DeliveryOutcome.DELIVERED -> {
                if (partIndex in job.deliveredParts) return@synchronized
                val updated = job.copy(deliveredParts = job.deliveredParts + partIndex, updatedAtMs = wallClock.nowMs())
                store.put(updated)
                emit(event(updated, "part_delivered", partIndex = partIndex, android = tpStatus))
                settle(updated, null)
            }
        }
    }

    /** Naikkan job ke SENT / DELIVERED bila semua part sudah lengkap. */
    private fun settle(job: SmsJob, android: Int?) {
        var current = job
        if (current.state == JobState.SENDING && current.sentParts.size >= current.partsTotal) {
            current = current.copy(state = JobState.SENT, updatedAtMs = wallClock.nowMs())
            store.put(current)
            emit(event(current, "sent", android = android))
        }
        if (current.state == JobState.SENT && current.deliveryReport && current.deliveredParts.size >= current.partsTotal) {
            current = current.copy(state = JobState.DELIVERED, updatedAtMs = wallClock.nowMs())
            store.put(current)
            emit(event(current, "delivered"))
        }
    }

    private fun fail(job: SmsJob, errorCode: String, android: Int?) {
        val failed = job.copy(state = JobState.FAILED, errorCode = errorCode, updatedAtMs = wallClock.nowMs())
        store.put(failed)
        emit(event(failed, "failed", error = errorCode, android = android))
    }

    /** Daftar job tak-terminal untuk `hello.inflight`. */
    fun inflight(): List<Inflight> = store.all().filter { !it.terminal }.map {
        Inflight(
            it.messageId, it.attempt, it.state.name.lowercase(), it.partsTotal, it.sentParts.size, it.deliveredParts.size,
        )
    }

    fun queueDepth(): Int = synchronized(lock) { queues.values.sumOf { it.size } }

    private fun event(
        job: SmsJob, state: String, partIndex: Int? = null, error: String? = null, android: Int? = null,
    ) = SmsEvent(
        eventId = newId(), messageId = job.messageId, attempt = job.attempt, state = state, partIndex = partIndex,
        partsTotal = job.partsTotal.coerceAtLeast(1), errorCode = error, androidResultCode = android, partsSent = job.sentParts.size,
        at = Time.iso(wallClock.nowMs()),
    )
}

/** Nilai `errorCode` pada ws-schema.json. */
val CONTRACT_ERROR_CODES = setOf(
    "invalid_destination", "short_code_blocked", "permission_denied", "radio_off", "no_service", "generic_failure", "null_pdu",
    "os_rate_limited", "sim_unavailable", "delivery_failed", "ack_timeout", "sent_timeout", "device_lost", "device_revoked", "expired", "suppressed",
)
