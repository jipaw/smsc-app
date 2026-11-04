package id.smsc.poc.core

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse

class ProxySpecTest {
    @Test fun parsesSocksWithCredentials() {
        val p = ProxySpec.parse("socks5://tsnet:ab%3Acd@127.0.0.1:41055")
        assertEquals(ProxySpec(ProxySpec.Type.SOCKS5, "127.0.0.1", 41055, "tsnet", "ab:cd"), p)
        assertEquals("socks5://tsnet:***@127.0.0.1:41055", p.toString())
        assertFalse(p.toString().contains("ab:cd"))
    }

    @Test fun parsesHttpWithoutCredentials() {
        assertEquals(ProxySpec(ProxySpec.Type.HTTP, "proxy.lan", 3128), ProxySpec.parse("http://proxy.lan:3128"))
        assertEquals(ProxySpec.Type.SOCKS5, ProxySpec.parse("socks5h://h:1").type)
    }

    @Test fun rejectsInvalid() {
        for (bad in listOf("ftp://h:1", "socks5://h", "socks5://:1", "socks5://h:1/x", "bukan url", "http://h:70000")) {
            assertFailsWith<IllegalArgumentException>(bad) { ProxySpec.parse(bad) }
        }
    }
}
