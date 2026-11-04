package id.smsc.poc

import android.content.Context
import id.smsc.poc.core.ProxySpec

/** Memastikan varian tanpa AAR (src/notsnet) punya API yang sama dengan yang dipakai PocService/MainActivity. */
@Suppress("unused")
internal fun tailnetParity(ctx: Context) {
    val available: Boolean = TailnetRuntime.AVAILABLE
    val summary: String = TailnetRuntime.summary
    val spec: ProxySpec = TailnetRuntime.start(ctx, "h", "k", "c")
    val status: String = TailnetRuntime.status()
    TailnetRuntime.stop()
}
