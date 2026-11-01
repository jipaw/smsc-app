# 01 — Analisis Kebutuhan dan Rekomendasi Stack

Status: **draft desain** · Dokumen ini menjadi dasar dokumentasi penuh. Kontrak ada di `contracts/`.

## 1. Ringkasan kebutuhan

| # | Kebutuhan | Implikasi desain |
|---|---|---|
| K1 | Aplikasi Android, minimal Android 10 | `minSdk = 29`. Batasan background, izin SMS, dan foreground service berbeda per versi (10 → 16) dan harus ditangani. |
| K2 | Konektivitas via Tailscale | Semua trafik app↔server hanya lewat tailnet; server tidak perlu diekspos ke internet publik. |
| K3 | App standby dan menerima perintah kapan saja | Foreground service + koneksi persisten yang dimulai dari device; bebas Doze/OEM-kill sebisa mungkin. |
| K4 | Setup ke server mudah, langkah minim | Enrollment via QR / deep link sekali pakai. |
| K5 | Gateway setup & manage client Android | Registry device, health, perintah jarak jauh, revoke, policy per device. |
| K6 | Multi SIM | Inventaris SIM per device, pemilihan SIM per pesan, antrean/jeda per SIM. |
| K7 | Dashboard: campaign | Wizard campaign, impor penerima, template variabel, start/pause/cancel, progres live. |
| K8 | Dashboard: kirim non-campaign | Halaman kirim cepat + API untuk sistem eksternal. |
| K9 | Dashboard: transaksi | Daftar/filter/detail/timeline per pesan, retry/cancel, ekspor. |
| K10 | Manajemen pengiriman (delay antar transaksi, dst.) | Dispatch policy berlapis (global → device → SIM → campaign), rate limit, jendela waktu, retry, TTL. |

Asumsi yang saya ambil (silakan koreksi, daftar lengkap di `05-roadmap-risiko-outline.md`):

- Skala target: puluhan device, ratusan SIM paling banyak, ≤ ~1 juta pesan/bulan. Throughput nyata dibatasi radio/operator, bukan server.
- Distribusi aplikasi **sideload/MDM**, bukan Google Play (Play membatasi izin SMS hanya untuk default SMS handler).
- Satu organisasi (single-tenant). Multi-tenant bukan target v1.
- HP gateway didedikasikan: selalu tersambung daya dan Wi-Fi/seluler, layar bisa mati.

## 2. Fakta platform yang mengubah desain

Ini bukan detail implementasi; masing-masing memengaruhi arsitektur.

1. **Izin `SEND_SMS` dibatasi untuk app sideload (Android 13+).** Pengguna harus mengizinkan "restricted settings" lewat *App info → ⋮ → Allow restricted settings* sebelum izin SMS bisa diberikan. Wizard onboarding harus memandu langkah ini.
2. **Batas kirim OS.** AOSP memantau jumlah SMS keluar per app (default ±30 pesan / 30 menit) dan dapat memunculkan dialog konfirmasi yang memblokir pengiriman tanpa pengguna. Ini menentukan throughput nyata, jadi policy default memakai *guard* konservatif (`os_sms_guard`) dan perlu diverifikasi di PoC (lihat R1). Mitigasi yang dievaluasi: menjadi default SMS app (`ROLE_SMS`), atau menaikkan batas via `adb`/MDM pada device khusus.
3. **Satu VPN aktif per HP.** Aplikasi Tailscale memakai slot VPN Android; HP gateway tidak bisa menjalankan VPN lain bersamaan. Konsekuensinya HP harus didedikasikan (atau opsi lanjut: Tailscale tertanam, §4.2).
4. **Foreground service wajib bertipe (Android 14+).** Dipakai `specialUse` (dengan justifikasi di manifest), bukan `dataSync` yang dibatasi durasi pada Android 15. `specialUse` boleh dimulai dari `BOOT_COMPLETED`.
5. **Doze/App Standby dan penghemat baterai vendor** (Xiaomi, Oppo, Vivo, Samsung "sleeping apps") dapat memutus koneksi walau ada foreground service. Wajib: pengecualian optimasi baterai untuk **app ini dan app Tailscale**, auto-start diizinkan, dan panduan per merek.
6. **Multi SIM.** `SubscriptionManager.getActiveSubscriptionInfoList()` (butuh `READ_PHONE_STATE`) dan `SmsManager.createForSubscriptionId(subId)`. `subscriptionId` bisa berubah saat SIM dicabut/eSIM diaktifkan, jadi identitas stabil SIM di sistem adalah `(device_id, slot_index)`; `subscriptionId` hanya hint yang di-refresh. ICCID tidak dapat dibaca oleh app non-privileged, jangan bergantung padanya.
7. **Status SMS.** `sentIntent` = diserahkan ke jaringan (bukan "sampai"), `deliveryIntent` = laporan operator, **tidak dijamin datang** (tergantung operator/penerima). Model status harus memisahkan `sent` dan `delivered`, dan menerima `sent` sebagai akhir yang sah setelah timeout.

## 3. Keputusan transport (bagaimana HP "menerima perintah kapan saja")

| Opsi | Cocok? | Alasan |
|---|---|---|
| FCM push | ✗ | Butuh Google Play Services dan internet publik; bertentangan dengan tujuan "semua via Tailscale", dan tidak andal untuk device tanpa GMS. |
| MQTT | ○ | Bagus untuk IoT, tetapi menambah broker dan model auth kedua. Tidak ada kebutuhan pub/sub banyak-ke-banyak. |
| gRPC bidirectional stream | ○ | Efisien, tetapi tooling Android + proxy (Tailscale Serve) lebih rumit untuk keuntungan kecil di skala ini. |
| Polling | ✗ | Latensi tinggi atau boros baterai; tidak memenuhi "kapan saja". |
| HP menjalankan server HTTP di tailnet | ✗ | Server harus menembus NAT/VPN HP, IP tailnet bisa berubah, port harus dijaga hidup; keamanan lebih buruk. |
| **WebSocket, dimulai dari device** | ✓ | Dua arah, satu koneksi, mature di OkHttp, mudah diproksi Tailscale Serve, mudah dikontrol (heartbeat, close code). Sisi server tidak perlu menjangkau HP. |

**Keputusan:** WebSocket persisten yang dibuka oleh aplikasi ke server (`wss://<host>.ts.net/ws/device`), dilindungi WireGuard (tailnet) + kredensial device. Detail di `contracts/device-ws-protocol.md`. REST tetap tersedia sebagai fallback (`POST /device/events`, `GET /device/config`).

## 4. Keputusan integrasi Tailscale

### 4.1 MVP — pakai aplikasi Tailscale resmi di HP (dipilih)

- HP login ke tailnet lewat app Tailscale (disarankan *tagged device* `tag:smsc-device`, key expiry dimatikan, dan "Always-on VPN" Android aktif).
- Server berada di tailnet (`tag:smsc-server`), diekspos hanya lewat **Tailscale Serve** sebagai HTTPS dengan nama MagicDNS: `https://smsc.<tailnet>.ts.net`. Sertifikat otomatis, sehingga tidak perlu izin *cleartext* di Android.
- ACL: `tag:smsc-device → tag:smsc-server:443` dan `group:smsc-admins → tag:smsc-server:443`; device tidak boleh berbicara satu sama lain.
- App mengecek `NetworkCapabilities.TRANSPORT_VPN` dan keterjangkauan server sebelum/selama onboarding, dan melaporkannya di heartbeat.

### 4.2 Lanjutan — Tailscale tertanam (`libtailscale`/tsnet), tidak diwajibkan sekarang

Menghilangkan kebutuhan slot VPN dan app kedua, tetapi menambah build Go (gomobile), ukuran APK, dan beban pemeliharaan. Disiapkan lewat abstraksi `ServerEndpoint`/`Transport` di app supaya dapat diganti tanpa mengubah protokol. Dipicu bila HP harus memakai VPN lain.

## 5. Rekomendasi stack

### 5.1 Aplikasi Android (client)

| Aspek | Pilihan | Catatan |
|---|---|---|
| Bahasa/UI | Kotlin 2.x, Jetpack Compose (Material 3) | UI hanya untuk onboarding, status, log lokal. |
| SDK | `minSdk 29`, `targetSdk` = stable terbaru | Uji di API 29, 31, 33, 34, 35+. |
| Arsitektur | Single-module awal; layer `data` / `domain` / `service`; Hilt | Cukup sederhana, modularisasi nanti bila perlu. |
| Jaringan | OkHttp (WebSocket + REST), kotlinx.serialization | Model pesan WS ditulis sebagai sealed class dan diuji terhadap contoh JSON di kontrak. |
| Penyimpanan | Room (`sms_job`, `outbox`), DataStore + Android Keystore (AEAD) untuk `device_secret` | `EncryptedSharedPreferences` sudah deprecated. |
| Background | Foreground service `specialUse` + `BOOT_COMPLETED` + WorkManager periodik sebagai *watchdog* (15 menit) | Watchdog memulihkan service yang dimatikan OEM. |
| SMS | `SmsManager.createForSubscriptionId`, `sendMultipartTextMessage`, `BroadcastReceiver` untuk sent/delivery | Receiver terdaftar di manifest agar tetap hidup lintas proses. |
| QR | CameraX + ML Kit Barcode (model bundled) | Fallback input kode manual. |
| Build | Gradle KTS + version catalog; distribusi APK bertanda tangan, update lewat dashboard (v1.1) | |

### 5.2 Backend (gateway server)

| Aspek | Pilihan | Alasan |
|---|---|---|
| Bahasa | **Go** | Satu biner statis, konkurensi ringan untuk ratusan WebSocket + scheduler, deploy sederhana di VM kecil/home-server di tailnet. |
| HTTP/WS | `chi` + `coder/websocket`, kode dihasilkan dari OpenAPI (`oapi-codegen`) | Kontrak-first: `contracts/openapi.yaml` adalah sumber kebenaran. |
| Database | **PostgreSQL 16** | Sumber kebenaran tunggal: data, antrean, lease. Antrean memakai `SELECT … FOR UPDATE SKIP LOCKED`, jadi **tidak perlu Redis/broker** pada skala ini. |
| Akses data | `pgx` + `sqlc`, migrasi `goose` | Query diketik, mudah diaudit. |
| Realtime ke dashboard | Server-Sent Events (`/events/stream`) | Satu arah cukup; auto-reconnect bawaan browser. |
| Job internal | Goroutine in-process (dispatcher, materializer campaign, reaper lease, retensi) dengan state di Postgres | Restart aman karena semua state ada di DB. |
| Observability | `slog` JSON, Prometheus `/metrics`, `/healthz`, `/readyz` | |
| Deploy | Docker Compose: `server`, `postgres`, `tailscale` (sidecar) atau Tailscale di host | Lihat dok. 04. |

### 5.3 Dashboard

React + TypeScript + Vite · TanStack Query/Router/Table · Tailwind + shadcn/ui · Recharts · tipe dihasilkan dari OpenAPI (`openapi-typescript`) · di-*embed* ke biner Go (`go:embed`) sehingga satu artefak.

### 5.4 Alternatif yang dipertimbangkan

| Alternatif | Kelebihan | Mengapa tidak dipilih |
|---|---|---|
| NestJS/Fastify + Prisma | Satu bahasa dengan dashboard | Runtime lebih berat, antrean/konkurensi lebih rawan; Go cukup produktif dengan codegen OpenAPI. Bisa diterima bila tim hanya menguasai TypeScript. |
| Kotlin/Ktor | Berbagi model dengan Android | Ekosistem dashboard/ops lebih kecil; tidak ada keuntungan nyata karena model dibagi lewat OpenAPI. |
| Laravel | CRUD cepat | WebSocket jangka panjang butuh Reverb/Octane tambahan; scheduler dengan lease presisi kurang natural. |
| Redis/BullMQ untuk antrean | Throughput antrean tinggi | Bottleneck adalah radio SIM (satuan pesan per detik), bukan DB. Postgres cukup dan satu sistem lebih sedikit. Dapat ditambah bila multi-instance server. |

### 5.5 Perkiraan kapasitas (untuk sizing, bukan janji)

Satu SIM dengan jeda 3–8 dtk + waktu kirim 1–3 dtk ≈ 8 dtk/pesan ≈ **±450 pesan/jam/SIM**. 10 SIM aktif ≈ 4.500/jam. Throughput ini dibatasi policy dan batas OS/operator; server sekelas 2 vCPU/2 GB jauh melebihi kebutuhan.

## 6. Prinsip desain turunan

1. **Server adalah sumber kebenaran; device adalah eksekutor yang bisa mati kapan saja.** Semua keputusan penjadwalan di server, device menegakkan guardrail sebagai pertahanan kedua.
2. **Hindari SMS ganda lebih penting daripada menghindari SMS tertunda.** Karena itu ada status `unknown` dan retry otomatis tidak dilakukan untuk kasus tak pasti.
3. **Setiap hal yang bisa hilang di jaringan punya id idempoten** (`message_id`, `event_id`, `Idempotency-Key`).
4. **Kontrak dulu**: perubahan perilaku dimulai dari `contracts/`, lalu kode dan dokumentasi mengikuti.
