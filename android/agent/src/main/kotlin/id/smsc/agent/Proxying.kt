package id.smsc.agent

import id.smsc.poc.core.ProxySpec
import okhttp3.Credentials
import okhttp3.OkHttpClient
import java.net.Authenticator
import java.net.InetSocketAddress
import java.net.PasswordAuthentication
import java.net.Proxy

/**
 * Terapkan [ProxySpec] ke OkHttp. SOCKS5: OkHttp memakai socket SOCKS JDK, yang meminta kredensial lewat
 * java.net.Authenticator; authenticator ini HANYA menjawab untuk host:port proxy kita dan protokol SOCKS.
 * Alamat tujuan dibiarkan tak ter-resolve sehingga nama MagicDNS di-resolve oleh node tailnet.
 * (Salinan identik ada di aplikasi Android: id.smsc.poc.Proxying.)
 */
fun OkHttpClient.Builder.applyProxy(spec: ProxySpec?): OkHttpClient.Builder {
    if (spec == null) return this
    val addr = InetSocketAddress.createUnresolved(spec.host, spec.port)
    when (spec.type) {
        ProxySpec.Type.SOCKS5 -> {
            proxy(Proxy(Proxy.Type.SOCKS, addr))
            if (spec.username != null) SocksAuth.register(spec)
        }
        ProxySpec.Type.HTTP -> {
            proxy(Proxy(Proxy.Type.HTTP, addr))
            val user = spec.username
            if (user != null) proxyAuthenticator { _, resp ->
                if (resp.request.header("Proxy-Authorization") != null) null
                else resp.request.newBuilder().header("Proxy-Authorization", Credentials.basic(user, spec.password ?: "")).build()
            }
        }
    }
    return this
}

/** Authenticator global JVM yang hanya melayani proxy SOCKS terdaftar (lainnya diteruskan ke authenticator sebelumnya = null). */
object SocksAuth : Authenticator() {
    @Volatile private var specs: List<ProxySpec> = emptyList()

    @Synchronized fun register(spec: ProxySpec) {
        specs = specs.filterNot { it.host == spec.host && it.port == spec.port } + spec
        setDefault(this)
    }

    override fun getPasswordAuthentication(): PasswordAuthentication? {
        if (!requestingProtocol.orEmpty().startsWith("SOCKS", ignoreCase = true)) return null
        val s = specs.firstOrNull { it.port == requestingPort && (it.host == requestingHost || requestingSite?.hostAddress == it.host) } ?: return null
        return PasswordAuthentication(s.username, (s.password ?: "").toCharArray())
    }
}
