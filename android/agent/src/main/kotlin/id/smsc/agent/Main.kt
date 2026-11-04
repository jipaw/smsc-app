package id.smsc.agent

import okhttp3.OkHttpClient
import java.io.File
import java.time.LocalTime
import java.time.format.DateTimeFormatter
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import kotlin.system.exitProcess

private val fmt = DateTimeFormatter.ofPattern("HH:mm:ss.SSS")
fun log(msg: String) = println("${LocalTime.now().format(fmt)} $msg").also { System.out.flush() }

private const val USAGE = """smsc-agent — agen perangkat SMSC headless (SMS disimulasikan)

Pemakaian:
  smsc-agent --enroll 'smsc://enroll?s=...&t=...' [--state DIR] [--sims N] [--latency-ms MS] [--ws-url URL]
  smsc-agent --enroll KODE --server https://smsc.tailnet.ts.net ...
  smsc-agent [--state DIR] ...            (pakai kredensial tersimpan)

Opsi:
  --state DIR       folder kredensial & job (default ./agent-state)
  --sims N          jumlah SIM simulasi (default 1)
  --latency-ms MS   jeda simulasi sent/delivered per bagian (default 300)
  --ws-url URL      timpa ws_url dari server (mis. ws://127.0.0.1:8080/ws/device saat uji lokal)

Nomor tujuan berakhiran 0000 / 0001 / 0002 / 0003 memicu gagal sinkron / gagal parsial / delivery gagal / macet."""

fun main(args: Array<String>) {
    val opts = HashMap<String, String>()
    var i = 0
    while (i < args.size) {
        val k = args[i]
        if (k == "-h" || k == "--help") { println(USAGE); return }
        if (!k.startsWith("--") || i + 1 >= args.size) { System.err.println(USAGE); exitProcess(2) }
        opts[k.removePrefix("--")] = args[i + 1]; i += 2
    }
    val stateDir = File(opts["state"] ?: "agent-state")
    val sims = opts["sims"]?.toInt() ?: 1
    val latency = opts["latency-ms"]?.toLong() ?: 300L
    val http = OkHttpClient.Builder().pingInterval(20, TimeUnit.SECONDS).readTimeout(0, TimeUnit.MILLISECONDS).build()
    val credStore = CredentialStore(File(stateDir, "credentials.json"))

    opts["enroll"]?.let { link ->
        val c = Enroller.enroll(http, EnrollLink.parse(link, opts["server"]), sims, AGENT_VERSION)
        credStore.save(c)
        log("enroll OK device_id=${c.deviceId} ws_url=${c.wsUrl}")
    }
    val creds = credStore.load() ?: run { System.err.println("belum ada kredensial di $stateDir — jalankan dengan --enroll"); exitProcess(2) }

    val exec = Executors.newScheduledThreadPool(2) { r -> Thread(r, "agent-sms").apply { isDaemon = true } }
    val gateway = SimulatedSmsGateway(sims, latency, exec) { log(it) }
    val client = AgentClient(
        http, creds, opts["ws-url"] ?: creds.wsUrl, sims, JsonFileJobStore(File(stateDir, "jobs.json")), gateway, exec,
        saveCreds = { credStore.save(it) }, log = ::log,
    )
    val done = CountDownLatch(1)
    Runtime.getRuntime().addShutdownHook(Thread { log("berhenti"); client.stop(); done.countDown() })
    log("agen mulai: device=${creds.deviceId} sims=$sims latensi=${latency}ms")
    client.start()
    // pantau penghentian karena 4401/4426
    while (!done.await(1, TimeUnit.SECONDS)) {
        if (client.stopped) { log("klien berhenti"); exitProcess(3) }
    }
}
