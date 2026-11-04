# Android PoC — SMSC Agent (skeleton)

Skeleton untuk PoC risiko R1/R2/R3/R5/R6 (lihat `docs/05` dan `poc/README.md`). **Bukan** aplikasi produksi.

| Modul | Isi | Dapat dibangun di |
|---|---|---|
| `:core` | Protokol WS v1, `CommandProcessor` (dedup, persist-before-act, antrean per slot, agregasi part), `SlotGate` (jeda/rate/os-guard), `Outbox`, `Backoff`, pemetaan error. JVM murni. | Mana saja (JDK 17+) |
| `:typecheck` | Meng-compile sumber `:app` terhadap API Android asli (`android-all`, API 34) + tes konstanta `SmsManager`. Tidak menghasilkan APK. | Mana saja (tanpa Android SDK) |
| `:agent` | Agen perangkat **headless** (aplikasi JVM): protokol WS v1 lengkap memakai `CommandProcessor`/`Outbox`/`SlotGate` dari `:core` dan OkHttp (sama dengan `:app`), enrollment dari tautan QR, rotasi kredensial, job persisten; **SMS disimulasikan**. Untuk menguji integrasi Kotlin↔smscd tanpa HP/emulator. | Mana saja (JDK 17+) |
| `tsnetbridge/` (Go) | Node Tailscale userspace (`tsnet`) untuk di-embed ke aplikasi lewat `gomobile bind`: proxy SOCKS5 loopback ber-kredensial, tanpa izin VPN. Lihat `tsnetbridge/README.md`. | AAR: mesin dengan NDK (winbuilder); tes Go: mana saja |
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
cd ../server && make tailnet                                                           # lab: agen --proxy → tsnet → tailnet → smscd
```

Nomor tujuan berakhiran `0000`/`0001`/`0002`/`0003` memicu gagal sinkron / gagal parsial (pesan multi-bagian) / delivery gagal / job macet (memancing `sms.status`).
Agen **tidak** menguji perilaku `SmsManager`, batas OS, laporan operator, Doze, atau multi-SIM nyata — itu tetap butuh HP (R1/R2/R3/R5/R6).

## Pendaftaran device (fase 47)

Buka **4. Koneksi → Daftarkan device**. Ada tiga cara:

- **Pindai QR** dari dashboard (Devices → Tambah device). Memakai Camera2 platform + ZXing (`QrDecoder` di `:core`), tanpa AndroidX atau Google Play services. Butuh izin kamera.
- **Deep link** `smsc://enroll?...`, misalnya dari kamera bawaan atau tautan yang disalin dashboard.
- **Tempel tautan / ketik kode manual** + URL server.

Perilaku:
- Selalu muncul konfirmasi yang menampilkan host server, karena tautan dari luar aplikasi bisa menunjuk server lain.
- Bila QR memuat auth key tailnet (`k=`) dan APK dibangun dengan `tsnetbridge.aar`, HP bergabung ke tailnet sendiri dulu, lalu enroll lewat tailnet. Bila server belum mengenali tag node, enroll dicoba ulang sebentar.
- Kredensial device disimpan terenkripsi AES-GCM dengan kunci **Android Keystore**, ditulis sinkron. `rotate_credential` kini didukung: secret baru disimpan dulu, baru dikonfirmasi ke server.
- Alur enrollment (`EnrollFlow`) ada di `:core` dan dipakai juga oleh agen JVM. `make agent` meng-enroll agen dari **gambar QR yang dirender seperti dashboard** (library `qrcode` yang sama, margin 1, lebar 240) dan menguji kode manual.

## Batasan skeleton yang disengaja
- Belum pernah dijalankan di perangkat fisik oleh pembuatnya; perilaku platform adalah hipotesis yang diuji PoC.
- Job `ACCEPTED` yang belum terkirim hilang dari antrean memori bila proses mati (pemulihan = pekerjaan implementasi sebenarnya).
- `ws://` cleartext diizinkan (dilindungi tailnet); tanpa Room/Hilt/Compose. Pemindai QR belum diuji di kamera HP nyata (decoder diuji dengan frame sintetis dan gambar QR dashboard).
- `type-check` memakai `android-all` yang menyertakan API `@hide`; pemakaian API tersembunyi tidak tertangkap.
