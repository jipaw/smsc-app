package id.smsc.poc

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import id.smsc.poc.core.DeviceCredentials
import id.smsc.poc.core.ProtocolJson
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Kredensial device (hasil enrollment) dienkripsi AES-256-GCM dengan kunci Android Keystore (tidak dapat diekspor).
 * Ditulis dengan commit() (sinkron): rotasi kredensial baru dikonfirmasi ke server setelah tersimpan (persist-before-ack).
 */
object CredentialStore {
    private const val ALIAS = "smsc_device_credentials"
    private const val PREFS = "cred"

    private fun key(): SecretKey {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (ks.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        gen.init(
            KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return gen.generateKey()
    }

    @Synchronized
    fun save(ctx: Context, c: DeviceCredentials): Boolean = runCatching {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.ENCRYPT_MODE, key()) }
        val ct = cipher.doFinal(ProtocolJson.encodeToString(DeviceCredentials.serializer(), c).toByteArray(Charsets.UTF_8))
        val blob = Base64.encodeToString(cipher.iv + ct, Base64.NO_WRAP)
        ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putString("blob", blob).commit()
    }.getOrElse { PocLog.i("cred", "gagal menyimpan kredensial: ${it.javaClass.simpleName}"); false }

    @Synchronized
    fun load(ctx: Context): DeviceCredentials? {
        val blob = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getString("blob", null) ?: return null
        return runCatching {
            val raw = Base64.decode(blob, Base64.NO_WRAP)
            val cipher = Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, raw, 0, 12)) }
            val json = String(cipher.doFinal(raw, 12, raw.size - 12), Charsets.UTF_8)
            ProtocolJson.decodeFromString(DeviceCredentials.serializer(), json)
        }.getOrElse { PocLog.i("cred", "kredensial tidak dapat dibuka (${it.javaClass.simpleName}); daftarkan ulang"); null }
    }

    @Synchronized
    fun clear(ctx: Context) {
        ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().remove("blob").commit()
    }
}
