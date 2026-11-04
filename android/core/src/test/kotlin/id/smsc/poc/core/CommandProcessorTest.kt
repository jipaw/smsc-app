package id.smsc.poc.core

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

class CommandProcessorTest {
    private val time = FakeTime()
    private val store = InMemoryJobStore()
    private val gateway = FakeGateway(store = store)
    private val events = mutableListOf<SmsEvent>()
    private var gateGap = 0L
    private var n = 0
    private val p = CommandProcessor(store, gateway, time, { SlotGate(MonotonicLike(time), minGapMs = gateGap) }, { events += it }, { "e${++n}" })

    /** Gate memakai jam palsu yang sama agar pengujian waktu deterministik. */
    private class MonotonicLike(val t: FakeTime) : TimeSource { override fun nowMs() = t.now }

    private fun states() = events.map { it.state }

    @Test fun `terima lalu kirim, persist sebelum SmsManager dipanggil`() {
        assertEquals(CommandAck(true, null), p.onSmsSend(cmd()))
        assertNull(p.pump())
        assertEquals(listOf<JobState?>(JobState.SENDING), gateway.stateSeenAtSend)   // D1
        assertEquals(1, gateway.sent.size)
        assertEquals(listOf("sending"), states())
    }

    @Test fun `sms send duplikat 5 kali hanya memanggil SmsManager sekali`() {
        val acks = (1..5).map { p.onSmsSend(cmd()) }
        p.pump()
        assertEquals(1, gateway.sent.size)
        assertTrue(acks[0].accepted)
        acks.drop(1).forEach { assertFalse(it.accepted); assertEquals("already_sending", it.reason) }
        p.onPartSent("m1", 1, 0, AndroidSms.RESULT_OK)           // job SENT
        assertEquals("duplicate", p.onSmsSend(cmd()).reason)       // setelah terminal-ish: duplicate
        p.pump(); assertEquals(1, gateway.sent.size)
    }

    @Test fun `attempt lebih baru diterima setelah attempt lama gagal`() {
        p.onSmsSend(cmd()); p.pump()
        p.onPartSent("m1", 1, 0, AndroidSms.ERROR_NO_SERVICE)
        assertTrue(p.onSmsSend(cmd(attempt = 2)).accepted)
        p.pump(); assertEquals(2, gateway.sent.size)
    }

    @Test fun `penolakan - kedaluwarsa, izin, sim`() {
        time.now = java.time.Instant.parse("2026-11-01T00:00:00Z").toEpochMilli()
        assertEquals("expired", p.onSmsSend(cmd(notAfter = "2026-10-31T00:00:00Z")).reason)
        gateway.permission = false; assertEquals("permission_denied", p.onSmsSend(cmd("a")).reason)
        gateway.permission = true; assertEquals("sim_unavailable", p.onSmsSend(cmd("b", slot = 5)).reason)
        assertEquals(0, store.all().size)
    }

    @Test fun `pesan 2 part - sent lalu delivered hanya bila semua part`() {
        gateway.parts = 2
        p.onSmsSend(cmd()); p.pump()
        p.onPartSent("m1", 1, 0, AndroidSms.RESULT_OK)
        assertFalse("sent" in states())
        p.onPartSent("m1", 1, 1, AndroidSms.RESULT_OK)
        assertEquals(listOf("sending", "part_sent", "part_sent", "sent"), states())
        p.onPartDelivered("m1", 1, 0, 0)
        assertFalse("delivered" in states())
        p.onPartDelivered("m1", 1, 1, 0)
        assertEquals("delivered", states().last())
        assertEquals(JobState.DELIVERED, store.get("m1")!!.state)
        assertTrue(events.all { it.partsTotal == 2 })
    }

    @Test fun `kegagalan satu part menggagalkan pesan dan part lain diabaikan`() {
        gateway.parts = 3
        p.onSmsSend(cmd()); p.pump()
        p.onPartSent("m1", 1, 0, AndroidSms.RESULT_OK)
        p.onPartSent("m1", 1, 1, AndroidSms.ERROR_NO_SERVICE)
        p.onPartSent("m1", 1, 2, AndroidSms.RESULT_OK)
        val last = events.last()
        assertEquals("failed", last.state); assertEquals("no_service", last.errorCode); assertEquals(4, last.androidResultCode)
        assertEquals(1, events.count { it.state == "failed" })
    }

    @Test fun `event sent ganda tidak menghasilkan event ganda`() {
        p.onSmsSend(cmd()); p.pump()
        p.onPartSent("m1", 1, 0, AndroidSms.RESULT_OK); p.onPartSent("m1", 1, 0, AndroidSms.RESULT_OK)
        assertEquals(1, events.count { it.state == "sent" })
    }

    @Test fun `laporan delivery - pending diabaikan, gagal permanen jadi delivery_failed`() {
        p.onSmsSend(cmd()); p.pump(); p.onPartSent("m1", 1, 0, AndroidSms.RESULT_OK)
        p.onPartDelivered("m1", 1, 0, 0x30)
        assertEquals("sent", states().last())
        p.onPartDelivered("m1", 1, 0, 0x40)
        assertEquals("delivery_failed", events.last().errorCode)
    }

    @Test fun `tanpa delivery report tidak ada event delivered`() {
        p.onSmsSend(cmd(dr = false)); p.pump(); p.onPartSent("m1", 1, 0, AndroidSms.RESULT_OK)
        p.onPartDelivered("m1", 1, 0, 0)
        assertEquals("sent", states().last()); assertFalse("delivered" in states())
    }

    @Test fun `kegagalan sinkron SmsManager menjadi event failed`() {
        gateway.failWith = SmsSendException("permission_denied", "x")
        p.onSmsSend(cmd()); p.pump()
        assertEquals("failed", events.last().state); assertEquals("permission_denied", events.last().errorCode)
        assertEquals(JobState.FAILED, store.get("m1")!!.state)
    }

    @Test fun `guardrail jeda antar pesan pada slot yang sama, slot lain paralel`() {
        gateGap = 3000
        p.onSmsSend(cmd("m1")); p.onSmsSend(cmd("m2")); p.onSmsSend(cmd("m3", slot = 1))
        val wait = p.pump()
        assertEquals(listOf("m1", "m3"), gateway.sent.map { it.messageId })   // slot 0 dan 1 masing-masing satu
        assertEquals(3000L, wait)
        time.advance(2999); assertEquals(1L, p.pump()); assertEquals(2, gateway.sent.size)
        time.advance(1); assertNull(p.pump())                      // m2 terkirim, antrean kosong -> tidak perlu timer
        assertEquals("m2", gateway.sent.last().messageId)
        assertEquals(0, p.queueDepth())
    }

    @Test fun `pesan kedaluwarsa saat menunggu gate menjadi failed expired tanpa dikirim`() {
        gateGap = 10_000
        time.now = java.time.Instant.parse("2026-11-01T00:00:00Z").toEpochMilli()
        p.onSmsSend(cmd("m1", notAfter = "2026-11-01T00:00:30Z")); p.onSmsSend(cmd("m2", notAfter = "2026-11-01T00:00:05Z"))
        p.pump(); time.advance(10_000); p.pump()
        assertEquals(listOf("m1"), gateway.sent.map { it.messageId })
        assertEquals("expired", events.last().errorCode)
    }

    @Test fun `cancel hanya untuk job yang belum sending`() {
        gateGap = 5000
        p.onSmsSend(cmd("m1")); p.onSmsSend(cmd("m2")); p.pump()
        assertEquals(CommandAck(true, null), p.onSmsCancel("m2"))
        assertEquals("already_sending", p.onSmsCancel("m1").reason)
        assertEquals(CommandAck(true, null), p.onSmsCancel("tidak-ada"))
        time.advance(5000); p.pump(); assertEquals(listOf("m1"), gateway.sent.map { it.messageId })
    }

    @Test fun `sms status dijawab dari penyimpanan lokal tanpa efek samping`() {
        gateGap = 0
        assertEquals(SmsState("tidak-ada", 1, "unknown"), p.onSmsStatus(SmsStatus("tidak-ada", 1)))
        p.onSmsSend(cmd()); p.pump()
        val sending = p.onSmsStatus(SmsStatus("m1", 1))
        assertEquals("sending", sending.state)
        assertNull(sending.errorCode)
        val sentBefore = gateway.sent.size; val eventsBefore = events.size
        repeat(3) { p.onSmsStatus(SmsStatus("m1", 1)) }
        assertEquals(sentBefore, gateway.sent.size)      // tidak mengirim ulang
        assertEquals(eventsBefore, events.size)           // tidak membuat event
        p.onSmsCancel("m1")                                // sudah sending: ditolak, status tetap
        assertEquals("sending", p.onSmsStatus(SmsStatus("m1", 1)).state)
    }

    @Test fun `sms status job gagal melaporkan error_code dalam enum kontrak`() {
        gateGap = 0
        store.put(SmsJob("f1", 1, JobState.FAILED, 0, 1, false, errorCode = "no_service"))
        store.put(SmsJob("f2", 1, JobState.FAILED, 0, 1, false, errorCode = "cancelled"))    // kode lokal non-kontrak
        assertEquals("no_service", p.onSmsStatus(SmsStatus("f1", 1)).errorCode)
        assertEquals("generic_failure", p.onSmsStatus(SmsStatus("f2", 1)).errorCode)
        store.put(SmsJob("d1", 2, JobState.DELIVERED, 0, 2, true, sentParts = setOf(0, 1), deliveredParts = setOf(0, 1)))
        val d = p.onSmsStatus(SmsStatus("d1", 2))
        assertEquals(SmsState("d1", 2, "delivered", 2, 2, 2, null), d)
    }

    @Test fun `inflight hanya memuat job tak-terminal`() {
        gateway.parts = 2
        p.onSmsSend(cmd("a")); p.onSmsSend(cmd("b")); p.pump()
        p.onPartSent("a", 1, 0, AndroidSms.RESULT_OK)
        val inflight = p.inflight().associateBy { it.messageId }
        assertEquals("sending", inflight["a"]!!.state); assertEquals(1, inflight["a"]!!.partsSent)
        p.onPartSent("b", 1, 0, AndroidSms.ERROR_GENERIC_FAILURE)
        assertFalse("b" in p.inflight().map { it.messageId })
    }
}
