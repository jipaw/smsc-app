import org.jetbrains.kotlin.gradle.dsl.JvmTarget

// Agen perangkat headless: protokol WS v1 + CommandProcessor/Outbox/SlotGate dari :core, SmsGateway SIMULASI.
// Untuk menguji integrasi Kotlin <-> smscd tanpa HP/emulator. Bukan pengganti uji SmsManager di HP nyata.
plugins {
    kotlin("jvm")
    kotlin("plugin.serialization")
    application
}

java {
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}
kotlin { compilerOptions { jvmTarget.set(JvmTarget.JVM_17) } }
tasks.withType<JavaCompile>().configureEach { options.release.set(17) }

application {
    mainClass.set("id.smsc.agent.MainKt")
    applicationName = "smsc-agent"
}

dependencies {
    implementation(project(":core"))
    implementation("com.squareup.okhttp3:okhttp:4.12.0") // sama dengan aplikasi Android
    testImplementation(kotlin("test"))
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
}

tasks.test {
    useJUnitPlatform()
    testLogging { events("passed", "failed", "skipped") }
}
