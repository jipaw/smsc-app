# Status implementasi

Per commit ini. "Teruji" menyebut bukti yang dijalankan, bukan klaim. Rujukan kebutuhan: `docs/spec/01`.

| Area | Status | Bukti |
|---|---|---|
| Kontrak REST (`openapi.yaml`, 63 operasi) | Valid; respons server dicocokkan ke skema | `server/tests/contract_check.py`: 67 respons, 0 ketidaksesuaian |
| Protokol WS (`ws-schema.json`) | Skema + 16 vector valid / 13 invalid; model Kotlin & server memakainya | `contracts/tools/validate_vectors.py`, `android :core` ProtocolVectorsTest, `poc/server/selftest.py` |
| DDL (`schema.sql`) | Snapshot akhir = migrasi 0001+0002, dibandingkan lewat `pg_dump` (uji positif dan negatif) | `server/tests/schema_sync.sh` |
| **Server fase 1+2** (Go) | FR-01, 02, 04–20 ✔ kecuali webhook (lihat `server/README.md`) | 30 tes integrasi (17 fase 1 + 13 campaign) + unit, lulus `-race`; 4 mutasi disengaja tertangkap |
| **Dashboard fase 1+2** (React) | + Campaigns: daftar, wizard 5 langkah, detail dan kontrol | typecheck, 5 unit, **61 pemeriksaan E2E** di Chromium (axe terang/gelap, mobile 390px, RBAC, wizard sampai selesai) |
| **Android PoC** | Skeleton; belum dijalankan di HP | `:core` 26 tes, `:typecheck` 2 tes, `poc/README.md` |
| Aplikasi Android produksi | **Belum** | menunggu hasil PoC (R1/R2/R3/R5/R6) |
| Variabel per-penerima pada "Kirim SMS" cepat | **Belum** (gunakan campaign) | – |
| Webhook (FR-21), retensi (FR-22), rotasi kredensial, metrik Prometheus, 2FA, multi-instance | **Belum** (fase 3/4); webhook = 501 | – |

## Pengukuran beban campaign (`make load`, 1 mesin, PG16 lokal, tanpa HP online)

| Operasi | Hasil |
|---|---|
| Impor CSV 50.000 baris (1,4 MB) | 2,6 dtk (±19.000 baris/dtk) |
| `preview` / `start` (validasi 50.000 penerima) | 143 ms / 129 ms |
| Materialisasi | ±2.000 pesan/dtk; **berhenti di batas antrean 20.000** (backpressure bekerja) |
| `GET /campaigns/{id}` (counter teragregasi) | median 43 ms, maks 85 ms |
| `GET /messages?campaign_id=` (halaman 50) | median 28 ms |
| Batalkan 50.000 pesan | 983 ms; invarian counter `total = Σ status` terjaga |

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
