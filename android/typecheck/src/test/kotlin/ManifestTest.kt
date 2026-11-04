import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue
import org.w3c.dom.Element

/**
 * Pemeriksaan statis AndroidManifest.xml :app (tanpa aapt/Android SDK):
 * setiap komponen ada sebagai kelas terkompilasi dengan tipe dasar yang benar, izin yang dipakai kode dideklarasikan,
 * dan deep link smsc://enroll mengarah ke EnrollActivity.
 */
class ManifestTest {
    private val appDir = File("../app/src")
    private val doc = DocumentBuilderFactory.newInstance().apply { isNamespaceAware = true }
        .newDocumentBuilder().parse(File(appDir, "main/AndroidManifest.xml"))
    private val ns = "http://schemas.android.com/apk/res/android"
    private fun Element.a(n: String) = getAttributeNS(ns, n)
    private val sources = appDir.walkTopDown().filter { it.extension == "kt" }.associate { it.nameWithoutExtension to it.readText() }

    private fun elements(tag: String) = doc.getElementsByTagName(tag).let { l -> (0 until l.length).map { l.item(it) as Element } }

    @Test fun componentsExistWithRightBaseClass() {
        val base = mapOf("activity" to "Activity()", "service" to "Service()", "receiver" to "BroadcastReceiver()")
        for ((tag, sup) in base) for (e in elements(tag)) {
            val simple = e.a("name").removePrefix(".")
            assertTrue(javaClass.classLoader.getResource("id/smsc/poc/$simple.class") != null, "$tag $simple tidak terkompilasi")
            val src = sources[simple] ?: sources.values.first { it.contains("class $simple") }
            assertTrue(Regex("class $simple\\s*:\\s*[\\w.]*${Regex.escape(sup)}").containsMatchIn(src), "$tag $simple harus turunan $sup")
        }
    }

    @Test fun permissionsUsedInCodeAreDeclared() {
        val declared = elements("uses-permission").map { it.a("name").removePrefix("android.permission.") }.toSet()
        val used = sources.values.flatMap { s -> Regex("Manifest\\.permission\\.([A-Z_]+)").findAll(s).map { it.groupValues[1] } }.toSet()
        assertTrue(used.contains("CAMERA"), "ScanActivity memakai izin kamera")
        assertEquals(emptySet(), used - declared, "izin dipakai kode tetapi tidak dideklarasikan")
    }

    @Test fun enrollDeepLinkRoutesToEnrollActivity() {
        val owners = elements("data").filter { it.a("scheme") == "smsc" }.map { (it.parentNode.parentNode as Element).a("name") }
        assertEquals(listOf(".EnrollActivity"), owners)
        val enroll = elements("activity").first { it.a("name") == ".EnrollActivity" }
        assertEquals("true", enroll.a("exported"))
        assertEquals("false", elements("activity").first { it.a("name") == ".ScanActivity" }.a("exported"), "ScanActivity tidak boleh diekspor")
    }
}
