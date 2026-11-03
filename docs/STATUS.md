# Status implementasi

Per commit ini. "Teruji" menyebut bukti yang dijalankan, bukan klaim. Rujukan kebutuhan: `docs/spec/01`.

| Area | Status | Bukti |
|---|---|---|
| Kontrak REST (`openapi.yaml`, 85 operasi) | Valid; respons server dicocokkan ke skema | `server/tests/contract_check.py`: 124 respons, 0 ketidaksesuaian |
| Protokol WS (`ws-schema.json`) | Skema + 17 vector valid / 15 invalid; model Kotlin & server memakainya | `contracts/tools/validate_vectors.py`, `android :core` ProtocolVectorsTest, `poc/server/selftest.py` |
| DDL (`schema.sql`) | Snapshot akhir = migrasi 0001–0004, dibandingkan lewat `pg_dump` (uji positif dan negatif) | `server/tests/schema_sync.sh` |
| **Server fase 1–5** (Go) | FR-01–26 ✔: + rotasi kredensial, 2FA TOTP, **kewajiban 2FA (default nonaktif)**, manajemen pengguna (peran, nonaktifkan, admin set password) (lihat `server/README.md`) | 65 tes integrasi (20 inti + 13 campaign + 13 fase 3 + 13 fase 4 + 6 fase 5) + 18 unit (termasuk vektor RFC 6238), lulus `-race`; mutasi disengaja pada 2FA/rotasi/gerbang/self-change tertangkap |
| **Dashboard fase 1–5** (React) | + Campaigns, Webhook, Retensi + tab **Keamanan** (kebijakan 2FA), manajemen pengguna di tabel, shell minimal bagi akun yang wajib 2FA, halaman **Akun** (ganti password, 2FA dengan QR & kode pemulihan), login dua langkah, tombol Putar (API key, secret webhook, kredensial device), Reset 2FA | typecheck, 5 unit, **105 pemeriksaan E2E** di Chromium (axe terang/gelap, mobile 390px, RBAC, wizard, HMAC webhook, `/metrics`, alur 2FA lengkap dengan TOTP dihitung di Node) |
| **Android PoC** | Skeleton; belum dijalankan di HP | `:core` 26 tes, `:typecheck` 2 tes, `poc/README.md` |
| Aplikasi Android produksi | **Belum** | menunggu hasil PoC (R1/R2/R3/R5/R6) |
| Variabel per-penerima pada "Kirim SMS" cepat | **Belum** (gunakan campaign) | – |
| WebAuthn/passkey, SSO, daftar password umum, hapus pengguna / undangan email, multi-instance | **Belum** | – |
| Rotasi kredensial **di sisi HP** (menyimpan `new_secret` ke Keystore lalu membalas `ok`) | Server + protokol + vektor siap & teruji dengan simulator; **belum diimplementasikan di aplikasi Android** (PoC membalas `ok=false`, aman: secret lama tetap berlaku) | `TestDeviceCredentialRotation*` |
| Alert Prometheus (`ops/prometheus/alerts.yml`) | Ditulis; setiap metrik yang dirujuk diverifikasi ada di `/metrics`. **Belum dijalankan di Prometheus/Alertmanager sungguhan** (ambang awal, perlu penyetelan) | `TestAlertRulesReferenceExistingMetrics` |

## Pengukuran beban campaign (`make load`, 1 mesin, PG16 lokal, tanpa HP online)

| Operasi | Hasil |
|---|---|
| Impor CSV 50.000 baris (1,4 MB) | 2,6 dtk (±19.000 baris/dtk) |
| `preview` / `start` (validasi 50.000 penerima) | 143 ms / 129 ms |
| Materialisasi | ±2.000 pesan/dtk; **berhenti di batas antrean 20.000** (backpressure bekerja) |
| `GET /campaigns/{id}` (counter teragregasi) | median 43 ms, maks 85 ms |
| `GET /messages?campaign_id=` (halaman 50) | median 28 ms |
| Batalkan 50.000 pesan | 983 ms; invarian counter `total = Σ status` terjaga |

## Fase 3 — perilaku yang perlu diketahui
- **Webhook = outbox transaksional**: baris pengiriman ditulis dalam transaksi yang sama dengan perubahan status (tidak hilang bila proses mati, tidak terkirim bila transaksi batal). Semantik *at-least-once*: penerima wajib dedup lewat `X-SMSC-Delivery` (id sama pada semua percobaan).
- Retry default 30 dtk, 2 mnt, 10 mnt, 1 jam, 6 jam setelah percobaan pertama = **6 percobaan** total (spec lama menulis "5×"); dapat diubah lewat `WEBHOOK_BACKOFF`. Gagal final dapat dikirim ulang manual (`409 webhook.not_retryable` bila belum gagal).
- **SSRF**: loopback, link-local/metadata cloud, unspecified, multicast ditolak (diperiksa saat membuat DAN saat dial, jadi DNS rebinding tidak lolos); jaringan privat/tailnet **diizinkan** (penerima internal adalah kasus utama); redirect tidak diikuti; env proxy diabaikan. `WEBHOOK_ALLOW_LOOPBACK=true` hanya untuk uji/dev.
- Secret penandatangan hanya tampil sekali saat dibuat dan disimpan terenkripsi AES-256-GCM (`WEBHOOK_ENC_KEY`, default diturunkan dari `SESSION_SECRET` → **merotasi SESSION_SECRET membuat secret lama tak terbaca**; atur `WEBHOOK_ENC_KEY` terpisah di produksi). Isi SMS tidak pernah ada di payload.
- **Retensi**: tiap jam (leader) membersihkan isi pesan terminal lebih tua dari `body_retention_days` (0 = segera), event pesan, variabel/nomor mentah penerima campaign selesai, sesi kedaluwarsa, enrollment lama, riwayat webhook. Audit log tidak dihapus. Pesan yang belum selesai tidak pernah disentuh. Pesan yang isinya sudah dibersihkan tidak bisa di-retry (422).
- **Hapus nomor** (`smscd privacy erase --number … [--suppress]`): anonimkan pesan, penerima, dan payload webhook untuk satu nomor; ditolak (`privacy.active_messages`) bila ada pesan aktif; audit hanya menyimpan hash.
- **Metrik** di listener terpisah `METRICS_ADDR` (default `127.0.0.1:9090`, bukan di port publik). Label berkardinalitas kecil (pola rute, bukan URL), tanpa nomor/isi pesan. Gauge DB di-cache 5 dtk. Nama metrik implementasi (mis. `smsc_message_transitions_total`) menjadi acuan; tabel di spec 10 §Monitoring disesuaikan.
- Bug yang tertangkap saat fase 3: `smsc_queue_depth` tidak punya seri saat antrean kosong (alert merujuk metrik yang tidak ada) → seri prioritas 0–9 selalu ada; retry delivery yang tak ada membalas 409 (kini 404); satu pemeriksaan E2E flaky (progress bar dihitung sebelum dirender).

## Fase 5 — perilaku yang perlu diketahui
- **Kewajiban 2FA default `off`**; menyala hanya bila admin memilih (`PUT /settings/security`). Berlaku seketika, termasuk untuk sesi yang sudah login; API key tidak terpengaruh. Admin harus sudah memakai 2FA sendiri untuk menyalakannya. Pembacaan kebijakan yang gagal → gerbang **fail-open** (keputusan sadar: lebih baik 2FA tidak ditegakkan sesaat daripada seluruh tim terkunci saat DB bermasalah). Cache kebijakan 2 dtk di memori (satu instance).
- **Mengubah akun sendiri ditolak** (peran, nonaktif, set password lewat endpoint admin): menjamin selalu ada admin aktif tanpa logika "admin terakhir" yang rawan balapan.
- Reset 2FA saat kebijakan menyala mengembalikan akun itu ke status digerbang (harus menyiapkan ulang) — disengaja.
- **Bug lama yang tertangkap tes fase 5**: menyimpan template tanpa variabel (`Halo semuanya`) membalas 500 (array `NULL` melanggar `NOT NULL`) — sudah diperbaiki + tes regresi. Kontrak: nilai enum `off` harus dikutip di YAML (`'off'`), kalau tidak parser YAML 1.1 membacanya sebagai `false`.

## Fase 4 — perilaku yang perlu diketahui
- **Rotasi secret device dua tahap**: server menyimpan secret baru sebagai *tertunda* dan mengirimnya (`device.command.new_secret`, WSS). Secret lama tetap sah; promosi terjadi saat HP membalas `ok=true` **atau** begitu secret baru dipakai untuk autentikasi (konfirmasi hilang di jalan tidak mengunci HP). `ok=false`, kedaluwarsa 10 mnt, atau device dicabut → secret tertunda tidak berlaku. Spec lama menaruh `new_secret` di hasil D→S; diubah (arah S→D, dan wajib/terlarang menurut `action` di JSON Schema) karena server yang menciptakan secret agar tidak ada status setengah-ganti.
- **2FA**: lihat spec 09. Kode TOTP yang dipakai saat aktivasi tidak bisa dipakai lagi untuk login (langkah yang sama), jadi login pertama setelah aktivasi butuh kode langkah berikutnya (≤ 30 dtk) — perilaku anti-replay, bukan bug. Hanya sesi dashboard yang bisa mengelola akun; API key → 403.
- **Rotasi API key** menjaga id/nama/scope; `prefix` berganti. **Rotasi secret webhook** default masa tumpang tindih 24 jam, header tanda tangan ganda.
- **Kunci server**: `SESSION_SECRET_PREVIOUS` membuat rotasi tanpa logout massal; `WEBHOOK_ENC_KEY_PREVIOUS` + `smscd secrets reencrypt` untuk kunci enkripsi (menutup catatan fase 3). Kunci yang salah gagal dengan jelas, bukan diam-diam.
- Pembatas laju login kini dapat dikonfigurasi (`LOGIN_RATE_PER_MIN`, `LOGIN_BURST`) karena banyak admin di balik satu NAT akan saling memblokir pada default 5/menit/IP.
- Bug yang tertangkap saat fase 4: tes menunjukkan kode TOTP aktivasi memang tidak boleh dipakai ulang (asumsi tes salah, bukan server); mutasi "tanpa sidik jari password" dan "tanpa promosi saat pemakaian pertama" tertangkap tes.

## Perilaku yang berbeda dari spesifikasi / keputusan yang diambil saat membangun
- Kill switch global ditambahkan ke kontrak (`GET/PUT /settings/dispatch-pause`); sebelumnya hanya disebut di spec 09/10.
- Timer reaper `sent_timeout` memakai policy **global**; ack timeout memakai policy efektif saat lease.
- `attempt` pada pesan `queued` pascakegagalan = nomor percobaan berikutnya.
- `Sim.registered/roaming` dan `subscription_id` nullable (belum dilaporkan HP); skema respons kini memuat `required`.
- Policy default bawaan membatasi kirim 08:00–20:00 Asia/Jakarta (sesuai docs/03); `00:00–00:00` = sepanjang hari.

## Bug yang tertangkap oleh pengujian (dan sudah diperbaiki)
0. (fase 2) Estimasi durasi memperlakukan batas harian sebagai laju per detik sehingga 6 pesan diperkirakan ±2 menit (seharusnya ±30 dtk) → fungsi murni `estimateDuration` + tes regresi. Wizard baru memeriksa variabel penerima setelah isi pesan diketahui (tidak mungkin saat impor). Skema respons `Campaign` perlu `required` eksplisit agar tipe UI benar.
1. Dispatcher bisa melanggar delay karena membaca snapshot lane yang basi (balapan dengan event `sent`) → pemeriksaan dipindah ke transaksi di bawah kunci SIM.
2. Kolom `actor` event melanggar CHECK saat pembuat pesan = `dashboard`/`user:email`.
3. Viewer salah ditolak membaca transaksi.
4. Kontrak terlalu longgar/ketat di beberapa skema (nullable, `required`, `/effective`).
5. UI: grafik overview kosong (CSS), QR baru sempat tampil "kedaluwarsa" (hitung mundur), kontras warna < 4,5:1.
6. Desain: PendingIntent hasil SMS harus `FLAG_MUTABLE` (hipotesis; PoC R5).
