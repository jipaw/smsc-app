package id.smsc.agent

import id.smsc.poc.core.JobStore
import id.smsc.poc.core.ProtocolJson
import id.smsc.poc.core.SmsJob
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import java.io.File
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import java.nio.file.attribute.PosixFilePermissions

/** Kredensial hasil enrollment (setara Keystore di HP). Disimpan di berkas mode 0600. */
@Serializable
data class Credentials(val deviceId: String, val deviceSecret: String, val wsUrl: String, val installId: String)

object Files0600 {
    fun write(file: File, text: String) {
        file.parentFile?.mkdirs()
        val tmp = File(file.path + ".tmp")
        tmp.writeText(text)
        runCatching { Files.setPosixFilePermissions(tmp.toPath(), PosixFilePermissions.fromString("rw-------")) }
        Files.move(tmp.toPath(), file.toPath(), StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE)
    }
}

class CredentialStore(private val file: File) {
    fun load(): Credentials? = runCatching { ProtocolJson.decodeFromString(Credentials.serializer(), file.readText()) }.getOrNull()
    fun save(c: Credentials) = Files0600.write(file, ProtocolJson.encodeToString(Credentials.serializer(), c))
}

/** JobStore persisten (setara FileJobStore aplikasi): job bertahan saat agen di-restart, untuk menguji rekonsiliasi hello.inflight. */
class JsonFileJobStore(private val file: File) : JobStore {
    private val map = LinkedHashMap<String, SmsJob>()
    private val serializer = ListSerializer(SmsJob.serializer())

    init {
        if (file.exists()) runCatching { ProtocolJson.decodeFromString(serializer, file.readText()).forEach { map[it.messageId] = it } }
    }

    @Synchronized override fun get(messageId: String): SmsJob? = map[messageId]
    @Synchronized override fun all(): List<SmsJob> = map.values.toList()
    @Synchronized override fun put(job: SmsJob) {
        map[job.messageId] = job
        Files0600.write(file, ProtocolJson.encodeToString(serializer, map.values.toList()))
    }
}
