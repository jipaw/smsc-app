// Modul Android PoC. Hanya dimuat bila Android SDK terdeteksi (lihat settings.gradle.kts).
val tsnetAar = file("libs/tsnetbridge.aar")
if (tsnetAar.exists()) logger.lifecycle("tsnetbridge.aar ditemukan: mode tailnet ter-embed AKTIF") else logger.lifecycle("tsnetbridge.aar tidak ada: build tanpa tailnet ter-embed")
plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "id.smsc.poc"
    compileSdk = 35

    defaultConfig {
        applicationId = "id.smsc.poc"
        minSdk = 29
        targetSdk = 35
        versionCode = 1
        versionName = "poc-0.1"
    }

    buildTypes {
        release {
            isMinifyEnabled = false   // PoC: tanpa R8
            signingConfig = signingConfigs.getByName("debug") // PoC: ditandatangani debug supaya mudah diinstal
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    lint { abortOnError = false }

    // Tailnet ter-embed: aktif bila AAR hasil `gomobile bind` (android/tsnetbridge) ada di app/libs.
    sourceSets["main"].kotlin.srcDir(if (tsnetAar.exists()) "src/tsnet/kotlin" else "src/notsnet/kotlin")
}

dependencies {
    implementation(project(":core"))
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    if (tsnetAar.exists()) implementation(files(tsnetAar))
}
