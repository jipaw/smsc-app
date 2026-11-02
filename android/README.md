# Android PoC — SMSC Agent (skeleton)

Skeleton untuk PoC risiko R1/R2/R3/R5/R6 (lihat `docs/05` dan `poc/README.md`). **Bukan** aplikasi produksi.

| Modul | Isi | Dapat dibangun di |
|---|---|---|
| `:core` | Protokol WS v1, `CommandProcessor` (dedup, persist-before-act, antrean per slot, agregasi part), `SlotGate` (jeda/rate/os-guard), `Outbox`, `Backoff`, pemetaan error. JVM murni. | Mana saja (JDK 17+) |
| `:typecheck` | Meng-compile sumber `:app` terhadap API Android asli (`android-all`, API 34) + tes konstanta `SmsManager`. Tidak menghasilkan APK. | Mana saja (tanpa Android SDK) |
| `:app` | Aplikasi: UI uji, foreground service, klien WebSocket, `SmsManager` nyata, receiver hasil kirim. | Mesin dengan Android SDK (mis. winbuilder) |

```bash
./gradlew :core:test :typecheck:test            # tanpa SDK
./gradlew :app:assembleDebug                    # butuh Android SDK (ANDROID_HOME / local.properties)
```

Windows (winbuilder): `powershell -File android\scripts\build-apk.ps1 [-Install]`.
`:app` otomatis dilewati bila SDK tidak terdeteksi (`settings.gradle.kts`).

## Batasan skeleton yang disengaja
- Belum pernah dijalankan di perangkat fisik oleh pembuatnya; perilaku platform adalah hipotesis yang diuji PoC.
- Job `ACCEPTED` yang belum terkirim hilang dari antrean memori bila proses mati (pemulihan = pekerjaan implementasi sebenarnya).
- Satu server/token, `ws://` cleartext (dilindungi tailnet), tanpa enrollment QR, tanpa Room/Hilt/Compose.
- `type-check` memakai `android-all` yang menyertakan API `@hide`; pemakaian API tersembunyi tidak tertangkap.
