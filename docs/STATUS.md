# Status implementasi

Per commit ini. "Teruji" menyebut bukti yang dijalankan, bukan klaim. Rujukan kebutuhan: `docs/spec/01`.

| Area | Status | Bukti |
|---|---|---|
| Kontrak REST (`openapi.yaml`, 62 operasi) | Valid; respons server dicocokkan ke skema | `server/tests/contract_check.py`: 44 respons, 0 ketidaksesuaian |
| Protokol WS (`ws-schema.json`) | Skema + 16 vector valid / 13 invalid; model Kotlin & server memakainya | `contracts/tools/validate_vectors.py`, `android :core` ProtocolVectorsTest, `poc/server/selftest.py` |
| DDL (`schema.sql`) | Dimuat di PG16; dipakai server sebagai migrasi (identik, dicek `verify.sh`) | tes integrasi |
| **Server fase 1** (Go) | FR-01, 02, 04, 05, 06, 07, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20 ✔ (lihat `server/README.md`) | 17 tes integrasi + unit, lulus `-race` |
| **Dashboard fase 1** (React) | Login, Overview, Devices+QR, SIM, Kirim SMS, Transaksi, Template, Pengaturan | typecheck, 5 unit, **37 pemeriksaan E2E** di Chromium (termasuk axe terang/gelap, mobile 390px, RBAC) |
| **Android PoC** | Skeleton; belum dijalankan di HP | `:core` 26 tes, `:typecheck` 2 tes, `poc/README.md` |
| Aplikasi Android produksi | **Belum** | menunggu hasil PoC (R1/R2/R3/R5/R6) |
| Campaign (FR-08/09/10), template per-penerima di kirim cepat | **Belum** (fase 2); endpoint 501 | – |
| Webhook (FR-21), retensi (FR-22), rotasi kredensial, metrik Prometheus, 2FA, multi-instance | **Belum** (fase 3/4) | – |

## Perilaku yang berbeda dari spesifikasi / keputusan yang diambil saat membangun
- Kill switch global ditambahkan ke kontrak (`GET/PUT /settings/dispatch-pause`); sebelumnya hanya disebut di spec 09/10.
- Timer reaper `sent_timeout` memakai policy **global**; ack timeout memakai policy efektif saat lease.
- `attempt` pada pesan `queued` pascakegagalan = nomor percobaan berikutnya.
- `Sim.registered/roaming` dan `subscription_id` nullable (belum dilaporkan HP); skema respons kini memuat `required`.
- Policy default bawaan membatasi kirim 08:00–20:00 Asia/Jakarta (sesuai docs/03); `00:00–00:00` = sepanjang hari.

## Bug yang tertangkap oleh pengujian (dan sudah diperbaiki)
1. Dispatcher bisa melanggar delay karena membaca snapshot lane yang basi (balapan dengan event `sent`) → pemeriksaan dipindah ke transaksi di bawah kunci SIM.
2. Kolom `actor` event melanggar CHECK saat pembuat pesan = `dashboard`/`user:email`.
3. Viewer salah ditolak membaca transaksi.
4. Kontrak terlalu longgar/ketat di beberapa skema (nullable, `required`, `/effective`).
5. UI: grafik overview kosong (CSS), QR baru sempat tampil "kedaluwarsa" (hitung mundur), kontras warna < 4,5:1.
6. Desain: PendingIntent hasil SMS harus `FLAG_MUTABLE` (hipotesis; PoC R5).
