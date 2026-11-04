package id.smsc.poc.core

import java.net.URI
import java.net.URLDecoder

/**
 * Proxy keluar untuk klien HTTP/WebSocket device: `socks5://user:pass@127.0.0.1:1055` (proxy loopback tsnet ter-embed)
 * atau `http://host:port`. Murni JVM; penerapan ke OkHttp ada di aplikasi/agen.
 * Dengan SOCKS5 nama host tujuan dikirim apa adanya ke proxy, sehingga nama MagicDNS (`smsc.<tailnet>.ts.net`)
 * di-resolve oleh node tailnet, bukan oleh DNS HP.
 */
data class ProxySpec(val type: Type, val host: String, val port: Int, val username: String? = null, val password: String? = null) {
    enum class Type { SOCKS5, HTTP }

    /** Tanpa password: aman untuk log. */
    override fun toString(): String {
        val auth = username?.let { "$it:***@" } ?: ""
        return "${type.name.lowercase()}://$auth$host:$port"
    }

    companion object {
        fun parse(raw: String): ProxySpec {
            val uri = try { URI(raw.trim()) } catch (e: Exception) { throw IllegalArgumentException("URL proxy tidak valid") }
            val type = when (uri.scheme?.lowercase()) {
                "socks5", "socks5h", "socks" -> Type.SOCKS5
                "http" -> Type.HTTP
                else -> throw IllegalArgumentException("skema proxy harus socks5:// atau http://")
            }
            val host = uri.host ?: throw IllegalArgumentException("host proxy kosong")
            if (uri.port !in 1..65535) throw IllegalArgumentException("port proxy wajib diisi")
            var user: String? = null
            var pass: String? = null
            uri.rawUserInfo?.let { ui ->
                val i = ui.indexOf(':')
                user = URLDecoder.decode(if (i < 0) ui else ui.substring(0, i), Charsets.UTF_8)
                pass = if (i < 0) null else URLDecoder.decode(ui.substring(i + 1), Charsets.UTF_8)
            }
            if (uri.rawPath?.let { it.isNotEmpty() && it != "/" } == true) throw IllegalArgumentException("URL proxy tidak boleh memuat path")
            return ProxySpec(type, host, uri.port, user, pass)
        }
    }
}
