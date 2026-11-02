package id.smsc.poc

import id.smsc.poc.core.SmsEvent

/** Statistik dari aliran event: jeda antar kirim, latensi sent/delivered, histogram error, deteksi stall (R1, R5). */
object Metrics {
    private class Msg(var sendingAt: Long? = null, var sentAt: Long? = null, var deliveredAt: Long? = null, var error: String? = null)

    private val msgs = LinkedHashMap<String, Msg>()

    @Synchronized fun reset() = msgs.clear()

    @Synchronized
    fun onEvent(ev: SmsEvent) {
        val now = System.currentTimeMillis()
        val m = msgs.getOrPut(ev.messageId) { Msg() }
        when (ev.state) {
            "sending" -> m.sendingAt = now
            "sent" -> m.sentAt = now
            "delivered" -> m.deliveredAt = now
            "failed" -> m.error = ev.errorCode ?: "unknown"
        }
    }

    @Synchronized
    fun summary(): String {
        if (msgs.isEmpty()) return "(belum ada event)"
        val now = System.currentTimeMillis()
        val sending = msgs.values.mapNotNull { it.sendingAt }.sorted()
        val gaps = sending.zipWithNext { a, b -> b - a }
        val sentLat = msgs.values.mapNotNull { m -> if (m.sendingAt != null && m.sentAt != null) m.sentAt!! - m.sendingAt!! else null }
        val delLat = msgs.values.mapNotNull { m -> if (m.sentAt != null && m.deliveredAt != null) m.deliveredAt!! - m.sentAt!! else null }
        val errors = msgs.values.mapNotNull { it.error }.groupingBy { it }.eachCount()
        val stalled = msgs.values.count { it.sendingAt != null && it.sentAt == null && it.error == null && now - it.sendingAt!! > 20_000 }
        fun stat(l: List<Long>) = if (l.isEmpty()) "-" else "min=${l.min()}ms avg=${l.sum() / l.size}ms max=${l.max()}ms"
        return buildString {
            appendLine("pesan=${msgs.size} sending=${sending.size} sent=${msgs.values.count { it.sentAt != null }} " +
                "delivered=${msgs.values.count { it.deliveredAt != null }} failed=${errors.values.sum()}")
            appendLine("jeda antar 'sending': ${stat(gaps)}")
            appendLine("latensi sending->sent: ${stat(sentLat)}")
            appendLine("latensi sent->delivered: ${stat(delLat)}")
            if (errors.isNotEmpty()) appendLine("error: $errors")
            if (stalled > 0) appendLine("PERINGATAN: $stalled pesan macet >20 dtk di 'sending' (indikasi dialog batas OS / R1)")
        }.trimEnd()
    }
}
