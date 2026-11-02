// Plugin Kotlin dipusatkan di sini (apply false) agar hanya dimuat sekali.
// Plugin Android (AGP) versinya ada di settings.gradle.kts > pluginManagement dan baru di-resolve
// saat :app dimuat (yaitu hanya jika Android SDK terdeteksi).
plugins {
    kotlin("jvm") version "2.0.21" apply false
    kotlin("plugin.serialization") version "2.0.21" apply false
}
