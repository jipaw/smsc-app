package id.smsc.poc.core

import java.io.File
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertTrue

/** Memastikan model Kotlin dapat mem-parse SEMUA test vector valid dari kontrak (T-CT-05, sisi decode). */
class ProtocolVectorsTest {
    private val dir = listOf("../../contracts/test-vectors/valid", "../contracts/test-vectors/valid")
        .map { File(it) }.first { it.isDirectory }

    private fun files() = dir.listFiles { f -> f.extension == "json" }!!.sortedBy { it.name }

    @Test fun `semua vector valid dapat di-decode ke model bertipe`() {
        assertTrue(files().size >= 16)
        for (f in files()) {
            val env = Codec.decode(f.readText())
            assertEquals(1, env.v, f.name)
            when (env.type) {
                MessageType.HELLO -> assertEquals(1, Codec.payload<Hello>(env).protocol)
                MessageType.HELLO_ACK -> assertNotNull(Codec.payload<HelloAck>(env).reconcile)
                MessageType.HEARTBEAT -> assertTrue(Codec.payload<Heartbeat>(env).battery.level in 0..100)
                MessageType.SMS_SEND -> assertTrue(Codec.payload<SmsSend>(env).to.startsWith("+"))
                MessageType.SMS_CANCEL -> Codec.payload<SmsCancel>(env)
                MessageType.SMS_STATUS -> assertTrue(Codec.payload<SmsStatus>(env).attempt >= 1)
                MessageType.SMS_STATE -> assertTrue(Codec.payload<SmsState>(env).state.isNotEmpty())
                MessageType.COMMAND_ACK -> Codec.payload<CommandAck>(env)
                MessageType.SMS_EVENT -> assertTrue(Codec.payload<SmsEvent>(env).partsTotal >= 1)
                MessageType.EVENT_ACK -> assertTrue(Codec.payload<EventAck>(env).eventIds.isNotEmpty())
                MessageType.DEVICE_COMMAND -> Codec.payload<DeviceCommand>(env)
                MessageType.DEVICE_COMMAND_RESULT -> Codec.payload<DeviceCommandResult>(env)
                MessageType.ERROR -> Codec.payload<ErrorPayload>(env)
                MessageType.CONFIG_UPDATE, "sim.report" -> Unit   // di luar cakupan PoC; cukup envelope valid
                else -> error("type tak dikenal di vector: ${env.type}")
            }
        }
    }

    @Test fun `round trip envelope menjaga field kontrak`() {
        val ev = SmsEvent("0192f3c0-7a1e-7c3e-9a55-000000000800", "0192f3c0-7a1e-7c3e-9a55-000000000100", 1, "sent", null, 2, null, -1, "2026-11-01T10:15:33.870Z")
        val wire = Codec.encode(Codec.envelope(MessageType.SMS_EVENT, ev))
        val back = Codec.payload<SmsEvent>(Codec.decode(wire))
        assertEquals(ev, back)
        assertTrue("\"ref\":null" in wire, wire)          // ref null tetap dikirim
        assertTrue("\"part_index\":null" in wire, wire)
    }

    @Test fun `field tak dikenal diabaikan dan bukan error`() {
        val env = Codec.decode("""{"v":1,"type":"command.ack","id":"x","ref":null,"ts":"2026-01-01T00:00:00Z","payload":{"accepted":true,"reason":null,"extra":1},"future":true}""")
        assertTrue(Codec.payload<CommandAck>(env).accepted)
    }
}
