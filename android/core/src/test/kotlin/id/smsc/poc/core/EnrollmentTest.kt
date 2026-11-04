package id.smsc.poc.core

import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

class EnrollmentTest {
    private val req = { _: String ->
        EnrollRequest("", "", DeviceInfoDto("Samsung", "A14", "14", 34, "poc-0.1"), Capabilities(batteryOptimizationIgnored = true),
            listOf(SimReport(0, 3, "Telkomsel", registered = true)))
    }

    @Test fun parsesLinks() {
        val l = EnrollLink.parse("smsc://enroll?s=https%3A%2F%2Fsmsc.contoh.ts.net%2F&t=T0K3N&k=tskey-auth-a%2Bb", null)
        assertEquals(EnrollLink("https://smsc.contoh.ts.net", "T0K3N", "tskey-auth-a+b"), l)
        assertEquals("smsc.contoh.ts.net", l.serverHost)
        assertFalse(l.toString().contains("T0K3N") || l.toString().contains("tskey"))
        assertEquals(EnrollLink("https://h.ts.net", "ABCD-1234"), EnrollLink.parse(" ABCD-1234 ", "https://h.ts.net/"))
        for (bad in listOf("smsc://lain?s=https%3A%2F%2Fh&t=x", "smsc://enroll?t=x", "smsc://enroll?s=ftp%3A%2F%2Fh&t=x", "smsc://enroll?s=https%3A%2F%2Fh", "https://contoh.id")) {
            assertFailsWith<IllegalArgumentException>(bad) { EnrollLink.parse(bad, null) }
        }
        assertFailsWith<IllegalArgumentException> { EnrollLink.parse("ABCD 1234", "https://h") }
        // QR berisi URL lain (bukan smsc://) tidak boleh dianggap kode manual, walau URL server tersedia
        val other = assertFailsWith<IllegalArgumentException> { EnrollLink.parse("https://contoh.id/promo", "https://h") }
        assertTrue(other.message!!.contains("bukan tautan enrollment"))
    }

    private class FakeTransport(vararg val replies: Pair<Int, String>) : EnrollTransport {
        val calls = mutableListOf<Triple<String, String, ProxySpec?>>()
        override fun postJson(url: String, json: String, proxy: ProxySpec?): Pair<Int, String> {
            calls += Triple(url, json, proxy)
            return replies[minOf(calls.size - 1, replies.size - 1)]
        }
    }

    private val ok = 201 to """{"device_id":"d1","device_secret":"smsd_x","ws_url":"wss://smsc.t.ts.net/ws/device","config":{}}"""

    @Test fun tailnetFirstThenEnrollThroughProxy() {
        val px = ProxySpec(ProxySpec.Type.SOCKS5, "127.0.0.1", 41000, "tsnet", "p")
        val keys = mutableListOf<String>()
        val t = FakeTransport(403 to """{"type":"device.tailnet_tag"}""", ok)
        val slept = mutableListOf<Long>()
        val r = EnrollFlow(t, { k -> keys += k; px }, sleep = { slept += it }).run(
            EnrollLink("https://smsc.t.ts.net", "TOK", "tskey-auth-1"), req, installId = "inst-1")
        assertEquals(listOf("tskey-auth-1"), keys)
        assertEquals(2, t.calls.size)
        assertEquals(listOf(2000L), slept)
        assertTrue(t.calls.all { it.first == "https://smsc.t.ts.net/api/v1/device/enroll" && it.third == px })
        assertEquals(DeviceCredentials("d1", "smsd_x", "wss://smsc.t.ts.net/ws/device", "inst-1", viaTailnet = true), r.credentials)
        val body = ProtocolJson.parseToJsonElement(t.calls[0].second).jsonObject
        assertEquals("TOK", body["token"]!!.jsonPrimitive.content)
        assertEquals("inst-1", body["install_id"]!!.jsonPrimitive.content)
        assertEquals(34, (body["device_info"] as JsonObject)["sdk_int"]!!.jsonPrimitive.content.toInt())
        assertFalse(r.credentials.toString().contains("smsd_x"))
    }

    @Test fun errorsAreMappedAndNotRetriedWithoutTailnet() {
        val t = FakeTransport(403 to """{"type":"device.tailnet_required"}""")
        val e = assertFailsWith<EnrollException> { EnrollFlow(t, null, sleep = {}).run(EnrollLink("https://h", "T", "tskey-auth-1"), req) }
        assertEquals("device.tailnet_required", e.problemType)
        assertEquals(1, t.calls.size) // tanpa tailnet ter-embed: tidak dicoba ulang, tanpa proxy
        assertNull(t.calls[0].third)
        val e401 = assertFailsWith<EnrollException> { EnrollFlow(FakeTransport(401 to "{}"), null).run(EnrollLink("https://h", "T"), req) }
        assertTrue(e401.message!!.contains("kedaluwarsa"))
        val down = assertFailsWith<EnrollException> { EnrollFlow({ _, _, _ -> throw java.net.UnknownHostException("smsc.t.ts.net") }, null).run(EnrollLink("https://smsc.t.ts.net", "T"), req) }
        assertTrue(down.message!!.contains("tidak terjangkau"))
        val tailFail = assertFailsWith<EnrollException> { EnrollFlow(FakeTransport(ok), { throw IllegalStateException("timeout") }).run(EnrollLink("https://h", "T", "k"), req) }
        assertTrue(tailFail.message!!.contains("tailnet gagal"))
    }

    @Test fun retriesAreBounded() {
        val t = FakeTransport(403 to """{"type":"device.tailnet_tag"}""")
        assertFailsWith<EnrollException> {
            EnrollFlow(t, { ProxySpec(ProxySpec.Type.SOCKS5, "127.0.0.1", 1) }, retries = 3, sleep = {}).run(EnrollLink("https://h", "T", "k"), req)
        }
        assertEquals(4, t.calls.size)
    }
}
