package id.smsc.agent

import id.smsc.poc.core.EnrollLink
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith

class AgentUnitTest {
    @Test fun partsGsmAndUcs2() {
        assertEquals(1, SmsParts.count("a".repeat(160)))
        assertEquals(2, SmsParts.count("a".repeat(161)))
        assertEquals(3, SmsParts.count("a".repeat(307)))
        assertEquals(2, SmsParts.count("€".repeat(81)))      // karakter ekstensi = 2 septet
        assertEquals(1, SmsParts.count("é".repeat(70).replace('é', 'ê')))   // ê bukan GSM → UCS-2, 70 muat
        assertEquals(2, SmsParts.count("ê".repeat(71)))
    }

    @Test fun enrollLinkParsing() {
        val l = EnrollLink.parse("smsc://enroll?s=https%3A%2F%2Fsmsc.contoh.ts.net&t=ABCD1234", null)
        assertEquals("https://smsc.contoh.ts.net", l.server)
        assertEquals("ABCD1234", l.token)
        assertEquals(EnrollLink("http://x", "K0DE1234"), EnrollLink.parse("K0DE1234", "http://x"))
        val k = EnrollLink.parse("smsc://enroll?s=https%3A%2F%2Fsmsc.contoh.ts.net&t=T0K&k=tskey-auth-abc%2Bdef", null)
        assertEquals("tskey-auth-abc+def", k.tailnetKey)
        kotlin.test.assertFalse(k.toString().contains("tskey") || k.toString().contains("T0K"))
        assertEquals(null, l.tailnetKey)
        assertFailsWith<IllegalArgumentException> { EnrollLink.parse("K0DE1234", null) }
        assertFailsWith<IllegalArgumentException> { EnrollLink.parse("smsc://enroll?t=X", null) }
    }
}
