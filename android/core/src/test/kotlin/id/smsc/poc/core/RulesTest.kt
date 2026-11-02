package id.smsc.poc.core

import kotlin.random.Random
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

class RulesTest {
    @Test fun `gate menegakkan jeda minimum`() {
        val t = FakeTime(); val g = SlotGate(t, minGapMs = 3000)
        assertEquals(0, g.waitMs()); g.record()
        assertEquals(3000, g.waitMs()); t.advance(1200); assertEquals(1800, g.waitMs())
        t.advance(1800); assertEquals(0, g.waitMs())
    }

    @Test fun `jeda acak selalu di dalam rentang min-max`() {
        val t = FakeTime(); val g = SlotGate(t, minGapMs = 3000, maxGapMs = 8000, random = Random(42))
        repeat(500) {
            g.record(); val w = g.waitMs()
            assertTrue(w in 3000..8000, "wait=$w"); t.advance(w)
        }
    }

    @Test fun `batas per menit memakai jendela geser`() {
        val t = FakeTime(); val g = SlotGate(t, maxPerMinute = 3)
        repeat(3) { assertEquals(0, g.waitMs()); g.record(); t.advance(10_000) }   // t=0,10,20 detik
        assertEquals(30_000, g.waitMs())                                           // sekarang t=30 detik; slot pertama bebas di t=60
        t.advance(30_000); assertEquals(0, g.waitMs())
    }

    @Test fun `os guard 25 per 30 menit`() {
        val t = FakeTime(); val g = SlotGate(t, osGuard = OsGuard(25, 1800))
        repeat(25) { assertEquals(0, g.waitMs()); g.record(); t.advance(1000) }
        val w = g.waitMs(); assertTrue(w > 0)
        assertEquals(1800_000L - 25_000L, w)   // pesan ke-1 dikirim 25 dtk lalu; bebas setelah 1800 dtk sejak itu
    }

    @Test fun `backoff full jitter dalam batas dan reset`() {
        val b = Backoff(1000, 60_000, Random(7))
        val caps = listOf(1000L, 2000, 4000, 8000, 16000, 32000, 60000, 60000)
        caps.forEach { cap -> assertTrue(b.next() in 0..cap) }
        b.reset(); assertTrue(b.next() <= 1000)
    }

    @Test fun `pemetaan error Android`() {
        assertEquals("no_service", ErrorMapper.map(AndroidSms.ERROR_NO_SERVICE).errorCode)
        assertEquals("radio_off", ErrorMapper.map(AndroidSms.ERROR_RADIO_OFF).errorCode)
        assertEquals("os_rate_limited", ErrorMapper.map(AndroidSms.ERROR_LIMIT_EXCEEDED).errorCode)
        assertEquals(false, ErrorMapper.map(AndroidSms.ERROR_NULL_PDU).retryableHint)
        assertEquals("short_code_blocked", ErrorMapper.map(AndroidSms.ERROR_SHORT_CODE_NEVER_ALLOWED).errorCode)
        assertEquals("generic_failure", ErrorMapper.map(999).errorCode)
    }

    @Test fun `klasifikasi TP-Status delivery`() {
        assertEquals(DeliveryOutcome.DELIVERED, DeliveryStatus.classify(0))
        assertEquals(DeliveryOutcome.PENDING, DeliveryStatus.classify(0x30))
        assertEquals(DeliveryOutcome.FAILED, DeliveryStatus.classify(0x40))
        assertEquals(DeliveryOutcome.FAILED, DeliveryStatus.classify(0x60))
    }

    @Test fun `UUIDv7 valid dan urut waktu`() {
        val re = Regex("^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")
        val a = Ids.v7(1_000_000_000_000L); val b = Ids.v7(1_000_000_000_001L)
        assertTrue(re.matches(a), a); assertTrue(re.matches(b), b); assertTrue(a < b)
    }

    @Test fun `outbox membuang event informatif lebih dulu saat penuh`() {
        val o = Outbox(max = 3)
        fun ev(id: String, s: String) = SmsEvent(id, "m", 1, s, null, 1, null, null, "2026-01-01T00:00:00Z")
        o.add(ev("e1", "sent")); o.add(ev("e2", "sending")); o.add(ev("e3", "failed")); o.add(ev("e4", "delivered"))
        assertEquals(listOf("e1", "e3", "e4"), o.pending().map { it.eventId })   // e2 (informatif) dibuang
        o.ack(listOf("e1")); assertEquals(2, o.size())
    }
}
