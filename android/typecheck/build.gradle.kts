import org.jetbrains.kotlin.gradle.dsl.JvmTarget

// Modul JVM yang meng-compile sumber :app terhadap API Android ASLI (android-all.jar milik Robolectric)
// supaya kode :app ter-type-check tanpa Android SDK. TIDAK menghasilkan APK dan tidak dijalankan.
// Batasan: android-all menyertakan API @hide, jadi pemakaian API tersembunyi tidak tertangkap di sini.
plugins {
    kotlin("jvm")
}

java {
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}
kotlin { compilerOptions { jvmTarget.set(JvmTarget.JVM_17) } }
tasks.withType<JavaCompile>().configureEach { options.release.set(17) }

val androidAll = "org.robolectric:android-all:14-robolectric-10818077" // API 34

// Sumber :app + varian tailnet ter-embed (src/tsnet) dikompilasi terhadap API Java ASLI hasil `gobind` dari android/tsnetbridge
// (src/gobind, dibuat ulang dengan android/tsnetbridge/gen-java.sh). Varian tanpa AAR (src/notsnet) dicek di source set terpisah.
sourceSets {
    main {
        kotlin.srcDir("../app/src/main/kotlin")
        kotlin.srcDir("../app/src/tsnet/kotlin")
        java.srcDir("src/gobind/java")
    }
    create("notsnet") { kotlin.srcDir("../app/src/notsnet/kotlin"); kotlin.srcDir("src/notsnetParity/kotlin") }
}

dependencies {
    implementation(project(":core"))
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    compileOnly(androidAll)
    testCompileOnly(androidAll)          // konstanta SmsManager di-inline saat kompilasi
    testImplementation(kotlin("test"))
    testImplementation(project(":core"))
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
    "notsnetImplementation"(project(":core"))
    "notsnetCompileOnly"(androidAll)
}

// varian tanpa AAR ikut dikompilasi pada setiap check/test
tasks.named("test") { dependsOn("compileNotsnetKotlin") }

tasks.test { useJUnitPlatform(); testLogging { events("passed", "failed", "skipped") } }
