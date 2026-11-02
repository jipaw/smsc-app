# Status implementasi

Per commit ini. "Teruji" menyebut bukti yang dijalankan, bukan klaim. Rujukan kebutuhan: `docs/spec/01`.

| Area | Status | Bukti |
|---|---|---|
| Kontrak REST (`openapi.yaml`, 71 operasi) | Valid; respons server dicocokkan ke skema | `server/tests/contract_check.py`: 82 respons, 0 ketidaksesuaian |
| Protokol WS (`ws-schema.json`) | Skema + 16 vector valid / 13 invalid; model Kotlin & server memakainya | `contracts/tools/validate_vectors.py`, `android :core` ProtocolVectorsTest, `poc/server/selftest.py` |
| DDL (`schema.sql`) | Snapshot akhir = migrasi 0001+0002+0003, dibandingkan lewat `pg_dump` (uji positif dan negatif) | `server/tests/schema_sync.sh` |
| **Server fase 1–3** (Go) | FR-01, 02, 04–22 ✔: + webhook (FR-21), retensi + hapus-nomor (FR-22), metrik Prometheus + 16 alert (lihat `server/README.md`) | 46 tes integrasi (20 inti + 13 campaign + 13 fase 3) + 15 unit, lulus `-race`; 4 mutasi disengaja tertangkap (fase 2) |
| **Dashboard fase 1–3** (React) | + Campaigns (wizard, kontrol) + Pengaturan > Webhook (buat, uji, riwayat, kirim ulang) dan Retensi | typecheck, 5 unit, **73 pemeriksaan E2E** di Chromium (axe terang/gelap, mobile 390px, RBAC, wizard, penerima webhook lokal memverifikasi HMAC, `/metrics`) |
| **Android PoC** | Skeleton; belum dijalankan di HP | `:core` 26 tes, `:typecheck` 2 tes, `poc/README.md` |
| Aplikasi Android produksi | **Belum** | menunggu hasil PoC (R1/R2/R3/R5/R6) |
| Variabel per-penerima pada "Kirim SMS" cepat | **Belum** (gunakan campaign) | – |
| Rotasi kredensial, 2FA, multi-instance | **Belum** (fase 4) | – |
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
