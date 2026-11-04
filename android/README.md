# Android PoC — SMSC Agent (skeleton)

Skeleton untuk PoC risiko R1/R2/R3/R5/R6 (lihat `docs/05` dan `poc/README.md`). **Bukan** aplikasi produksi.

| Modul | Isi | Dapat dibangun di |
|---|---|---|
| `:core` | Protokol WS v1, `CommandProcessor` (dedup, persist-before-act, antrean per slot, agregasi part), `SlotGate` (jeda/rate/os-guard), `Outbox`, `Backoff`, pemetaan error. JVM murni. | Mana saja (JDK 17+) |
| `:typecheck` | Meng-compile sumber `:app` terhadap API Android asli (`android-all`, API 34) + tes konstanta `SmsManager`. Tidak menghasilkan APK. | Mana saja (tanpa Android SDK) |
| `:agent` | Agen perangkat **headless** (aplikasi JVM): protokol WS v1 lengkap memakai `CommandProcessor`/`Outbox`/`SlotGate` dari `:core` dan OkHttp (sama dengan `:app`), enrollment dari tautan QR, rotasi kredensial, job persisten; **SMS disimulasikan**. Untuk menguji integrasi Kotlin↔smscd tanpa HP/emulator. | Mana saja (JDK 17+) |
| `:app` | Aplikasi: UI uji, foreground service, klien WebSocket, `SmsManager` nyata, receiver hasil kirim. | Mesin dengan Android SDK (mis. winbuilder) |

```bash
./gradlew :core:test :typecheck:test :agent:test   # tanpa SDK
./gradlew :app:assembleDebug                    # butuh Android SDK (ANDROID_HOME / local.properties)
```

Windows (winbuilder): `powershell -File android\scripts\build-apk.ps1 [-Install]`.
`:app` otomatis dilewati bila SDK tidak terdeteksi (`settings.gradle.kts`).

## Agen perangkat JVM (`:agent`)

```bash
./gradlew :agent:installDist
agent/build/install/smsc-agent/bin/smsc-agent --enroll 'smsc://enroll?s=...&t=...'   # tautan dari Devices → Tambah device
agent/build/install/smsc-agent/bin/smsc-agent                                         # berikutnya: pakai kredensial tersimpan (agent-state/)
cd ../server && make agent                                                             # E2E: agen ↔ biner smscd nyata (17 pemeriksaan)
```

Nomor tujuan berakhiran `0000`/`0001`/`0002`/`0003` memicu gagal sinkron / gagal parsial (pesan multi-bagian) / delivery gagal / job macet (memancing `sms.status`).
Agen **tidak** menguji perilaku `SmsManager`, batas OS, laporan operator, Doze, atau multi-SIM nyata — itu tetap butuh HP (R1/R2/R3/R5/R6).

## Batasan skeleton yang disengaja
- Belum pernah dijalankan di perangkat fisik oleh pembuatnya; perilaku platform adalah hipotesis yang diuji PoC.
- Job `ACCEPTED` yang belum terkirim hilang dari antrean memori bila proses mati (pemulihan = pekerjaan implementasi sebenarnya).
- Satu server/token, `ws://` cleartext (dilindungi tailnet), tanpa enrollment QR, tanpa Room/Hilt/Compose. (Enrollment dari tautan QR dan rotasi kredensial sudah ada di `:agent` sebagai acuan implementasi.)
- `type-check` memakai `android-all` yang menyertakan API `@hide`; pemakaian API tersembunyi tidak tertangkap.
