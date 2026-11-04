# Review operasional, keamanan, dan kemudahan instalasi — SMSC Gateway

Tanggal: 4 November 2026 · Basis: commit `48f81dc` (branch `claude/great-dijkstra-frg4xa`) · Cakupan: server Go, dashboard React, kontrak, ops, PoC Android.

## Ringkasan

Server dan dashboard sudah matang dan teruji luas (tes integrasi `-race`, kontrak OpenAPI, sinkronisasi skema, ±200 pemeriksaan E2E). Tiga hal masih menghambat sistem berjalan optimal di lapangan:

1. **Kemampuan inti belum terbukti**: aplikasi Android baru skeleton PoC dan belum pernah mengirim SMS dari HP nyata.
2. **Pembatasan laju salah sasaran di belakang Tailscale Serve**: semua klien terlihat sebagai `127.0.0.1`, sehingga batas login berlaku untuk seluruh pengguna sekaligus dan dapat disalahgunakan untuk memblokir login.
3. **Instalasi masih manual**: 72 variabel env, tanpa paket (Docker/systemd), tanpa `.env.example`, panduan tersebar.

## Alur sistem yang berjalan

1. **Pendaftaran HP**: admin membuat enrollment → dashboard menampilkan QR/tautan → HP menukarnya dengan device secret. Server hanya mendengarkan di `127.0.0.1`; `tailscale serve` mengeksposnya ke tailnet sebagai HTTPS.
2. **Sambungan HP**: WebSocket `/ws/device`; setiap sambung ulang, `hello` membawa pesan yang sedang diproses untuk dicocokkan.
3. **Pengiriman**: leader (advisory lock PostgreSQL) menjalankan dispatcher → pesan di-lease ke satu SIM dengan jeda/rate dari policy → `sms.send` → `command.ack` → event `sent/delivered/failed` → webhook lewat outbox.
4. **Pengaman**: reaper (batas waktu), tanya status ke HP (`sms.status`), pemulihan saat leader berganti, gagal parsial tidak diantre ulang, kill switch, retensi, metrik/alert.

Semua status ada di database, sehingga restart dan pergantian leader aman.

## Temuan dan rekomendasi

| # | Prioritas | Temuan | Dampak | Rekomendasi | Status |
|---|---|---|---|---|---|
| 1 | Kritis | Aplikasi Android belum pernah dijalankan di HP (QR enrollment & rotasi kredensial belum ada di aplikasi; `SmsManager` hanya lolos typecheck) | Pengiriman SMS sungguhan belum terbukti | PoC di HP nyata lewat winbuilder; dari sesi ini: agen JVM yang memakai `:core` terhadap server nyata (lihat bawah) | Agen JVM selesai (17/17 E2E); uji HP nyata menunggu winbuilder |
| 2 | Tinggi | Rate limit login memakai `RemoteAddr` → di belakang Tailscale Serve semua klien = `127.0.0.1`; batas 5/menit berlaku global | Beberapa orang login bersamaan kena 429; siapa pun di tailnet dapat memblokir semua login | Identitas klien dari proxy tepercaya (`X-Forwarded-For` dari `TRUSTED_PROXIES`, `Tailscale-User-Login` bila `TRUST_TAILSCALE_HEADERS=true`) | Selesai |
| 3 | Tinggi | Penguncian akun (5 gagal → 15 menit) berlaku dari sumber mana pun | Akun admin dapat dikunci orang lain yang tahu emailnya | Penguncian per (akun, sumber); penguncian akun global hanya sebagai jaring pengaman dengan ambang lebih tinggi (50) | Selesai |
| 4 | Sedang | `TRUST_TAILSCALE_HEADERS` didokumentasikan tetapi tidak dipakai kode | Operator mengira identitas Tailscale dipakai | Implementasikan (bagian dari #2) | Selesai |
| 5 | Sedang | `REQUIRE_VPN` hanya laporan HP sendiri | Bukan kontrol keamanan | Dokumentasikan ACL Tailscale minimal sebagai syarat instalasi | Selesai (`INSTALL.md` §0) |
| 6 | Sedang | Tidak ada batas tujuan / kuota per API key atau pengguna | Biaya SMS internasional/premium tak terkendali bila key bocor | Allow-list awalan tujuan (`ALLOWED_DESTINATION_PREFIXES`); kuota harian per key/pengguna menyusul | Allow-list selesai; kuota menyusul |
| 7 | Sedang | Kunci enkripsi diturunkan dari `SESSION_SECRET` bila `WEBHOOK_ENC_KEY` kosong; rahasia hanya lewat env | Bocornya satu env membuka sesi dan data terenkripsi | Dukungan `*_FILE` untuk rahasia; `smscd doctor` memperingatkan kunci turunan | Selesai |
| 8 | Rendah | `audit_logs` dan baris `email_outbox` tumbuh tanpa batas | Database membesar perlahan | `AUDIT_RETENTION_DAYS` (default 0 = simpan selamanya), pembersihan outbox email lama | Selesai |
| 9 | Rendah | 2FA default mati | Akun admin hanya dilindungi password | **Tetap mati sesuai keputusan pemilik**; `smscd doctor` memberi saran | Ditunda (keputusan) |
| 10 | Rendah | Alert dan dashboard Grafana hanya diperiksa statis | Salah sintaks/ambang tak ketahuan | Alert lonjakan 429; validasi promtool/Grafana dari jaringan yang sama | Alert 429 dan penguncian login selesai; validasi di Prometheus nyata belum |

## Temuan tambahan saat implementasi

- **Klien Android tidak berhenti saat kredensial ditolak di handshake.** Protokol menyatakan secret tidak valid → HTTP 401 saat upgrade, tetapi `GatewayClient` hanya berhenti pada close code 4401. Akibatnya HP yang di-revoke atau memakai secret lama terus mencoba ulang. Ditemukan oleh agen JVM dan diperbaiki di aplikasi.
- `TRUST_TAILSCALE_HEADERS` di `docs/04` disebut sebagai SSO tanpa password. Implementasi saat ini hanya memakainya sebagai identitas klien untuk rate limit dan penguncian, bukan untuk login.

## Kemudahan instalasi

- Paket: `Dockerfile` multi-stage (Go + web ter-embed, image 36 MB, nonroot), `deploy/compose` (PostgreSQL + sidecar Tailscale, rahasia lewat berkas, `init-secrets.sh`), `deploy/systemd` (unit berpengerasan dengan `LoadCredential`), `.env.example` minimal. Image dan compose sudah dijalankan di sesi ini dengan Tailscale diganti stub; sidecar Tailscale sungguhan belum diuji.
- `smscd doctor`: memeriksa konfigurasi wajib, koneksi DB dan migrasi, panjang/sumber rahasia, `PUBLIC_BASE_URL` vs `WEBAUTHN_RP_ID`, alamat bind, SMTP/OIDC, allow-list tujuan, dan 2FA (saran).
- Satu `INSTALL.md`: dari nol sampai SMS pertama, termasuk ACL Tailscale dan backup.

## PoC Android: alternatif dari sesi ini

Yang **tidak** dapat dijalankan dari sesi cloud ini:

- Emulator Android: tidak ada `/dev/kvm` (emulator tanpa akselerasi tidak praktis).
- Membangun APK: unduhan Android SDK (`dl.google.com`) ditolak proxy jaringan sesi ini.
- HP fisik atau winbuilder: tidak terjangkau dari container cloud.

Yang **dapat** dijalankan dari sesi ini:

- **Agen perangkat JVM** (`android/agent`, `make agent`): memakai kode `:core` yang sama dengan aplikasi (CommandProcessor, Outbox, SlotGate, Codec) dan klien WebSocket OkHttp, tersambung ke `smscd` sungguhan. Menguji integrasi Kotlin↔Go end-to-end (hello/rekonsiliasi, `sms.send` → ack → event, multi-bagian, `sms.status`, sambung ulang) dengan `SmsGateway` palsu. Tidak menggantikan uji `SmsManager` di HP.

Yang tetap perlu HP nyata (R1/R2/R3/R5/R6): perilaku `SmsManager` dan batas OS, laporan delivery operator, Doze/foreground service, multi-SIM, ketahanan baterai/jaringan.

## Urutan pengerjaan

1. ✔ Identitas klien dan rate limit login (#2–#4).
2. ✔ Paket instalasi, `.env.example`, `smscd doctor`, `INSTALL.md`, `*_FILE` (#7).
3. ✔ Allow-list tujuan, retensi audit, alert 429 (#6, #8, #10).
4. ✔ Agen perangkat JVM (#1, sebagian). PoC di HP nyata lewat winbuilder dikerjakan pemilik.
5. Menyusul: kuota harian per key/pengguna, uji sidecar Tailscale sungguhan, validasi promtool/Grafana dari jaringan yang sama.
