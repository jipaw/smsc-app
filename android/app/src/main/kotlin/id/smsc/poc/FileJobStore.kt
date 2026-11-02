package id.smsc.poc

import id.smsc.poc.core.JobStore
import id.smsc.poc.core.ProtocolJson
import id.smsc.poc.core.SmsJob
import kotlinx.serialization.builtins.ListSerializer
import java.io.File

/** JobStore sederhana: seluruh peta ditulis ke berkas JSON tiap perubahan (cukup untuk skala PoC). */
class FileJobStore(private val file: File) : JobStore {
    private val map = LinkedHashMap<String, SmsJob>()
    private val serializer = ListSerializer(SmsJob.serializer())

    init {
        runCatching {
            if (file.exists()) {
                val cutoff = System.currentTimeMillis() - 7L * 24 * 3600 * 1000
                ProtocolJson.decodeFromString(serializer, file.readText())
                    .filter { !it.terminal || it.updatedAtMs > cutoff }
                    .forEach { map[it.messageId] = it }
            }
        }.onFailure { PocLog.i("store", "gagal memuat job: $it") }
    }

    @Synchronized override fun get(messageId: String): SmsJob? = map[messageId]
    @Synchronized override fun all(): List<SmsJob> = map.values.toList()
    @Synchronized override fun put(job: SmsJob) { map[job.messageId] = job; persist() }

    private fun persist() {
        runCatching {
            val tmp = File(file.path + ".tmp")
            tmp.writeText(ProtocolJson.encodeToString(serializer, map.values.toList()))
            if (!tmp.renameTo(file)) { file.writeText(tmp.readText()); tmp.delete() }
        }.onFailure { PocLog.i("store", "gagal menyimpan job: $it") }
    }
}
