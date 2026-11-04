pluginManagement {
    repositories {
        gradlePluginPortal()
        google()
        mavenCentral()
    }
    plugins {
        id("com.android.application") version "8.7.3"
    }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        // Mirror Maven Central milik Google (isi identik mavenCentral()); dipakai karena host default kadang kena rate limit.
        maven("https://maven-central.storage-download.googleapis.com/maven2/")
        google {
            content {
                includeGroupByRegex("com\\.android.*")
                includeGroupByRegex("com\\.google.*")
                includeGroupByRegex("androidx.*")
            }
        }
    }
}

rootProject.name = "smsc-poc"

// :core, :typecheck, dan :agent selalu ada (JVM murni, tidak butuh Android SDK).
// :agent = agen perangkat headless (JVM) yang memakai :core terhadap smscd nyata dengan SmsGateway simulasi.
include(":core", ":typecheck", ":agent")

// :app hanya dimuat bila Android SDK terdeteksi (local.properties / ANDROID_HOME).
val hasAndroidSdk = file("local.properties").exists() ||
    System.getenv("ANDROID_HOME") != null || System.getenv("ANDROID_SDK_ROOT") != null
if (hasAndroidSdk) include(":app") else logger.lifecycle("Android SDK tidak ditemukan: modul :app dilewati (hanya :core dan :typecheck).")
