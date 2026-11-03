# Spec 01 — Product Requirements (PRD)

Versi 0.1 · Basis: `docs/01`–`05`. ID kebutuhan (FR/NFR) dipakai silang di semua dokumen spec dan rencana uji.

## 1. Tujuan dan non-tujuan

**Tujuan.** Menyediakan SMS gateway privat: HP Android berperan sebagai modem SMS multi-SIM yang dikendalikan server melalui Tailscale; operator mengelola perangkat, campaign, pengiriman non-campaign, dan memantau transaksi dari dashboard; sistem lain dapat mengirim lewat API.

**Non-tujuan (v1).** SMS masuk/percakapan dua arah, MMS/RCS, multi-tenant, penagihan, distribusi lewat Google Play, penyedia SMS-A2P/SMPP.

## 2. Persona

| Persona | Peran | Kebutuhan utama |
|---|---|---|
| **Admin** | Memiliki sistem | Mendaftarkan HP, mengatur policy, pengguna, API key, melihat audit |
| **Operator** | Operasional harian | Membuat campaign, kirim cepat, memantau transaksi, retry/cancel |
| **Integrator** | Developer sistem lain | API kirim yang idempoten, status per pesan, webhook (fase 3) |
| **Teknisi HP** | Menyiapkan HP gateway | Setup cepat, checklist jelas, tahu jika ada yang salah |
| **Viewer** | Auditor/manajemen | Baca laporan; nomor termasking |

## 3. Kebutuhan fungsional

Prioritas: **M** = MVP (fase 1), **S** = fase 2, **C** = fase 3. Kriteria penerimaan (KP) ditulis uji-able.

### 3.1 Device dan SIM

| ID | Kebutuhan | Pri | Kriteria penerimaan |
|---|---|---|---|
| FR-01 | Enrollment device lewat QR/kode sekali pakai | M | KP1 token kedaluwarsa 15 mnt & sekali pakai · KP2 setelah scan, device `online` di dashboard < 10 dtk · KP3 token dipakai ulang → 401 · KP4 setup ≤ 4 interaksi di HP setelah Tailscale tersambung |
| FR-02 | Registry & status device (`pending/online/degraded/offline/revoked`) dengan baterai, jaringan, versi app, capability | M | KP1 offline terdeteksi ≤ 90 dtk · KP2 `degraded` bila baterai < 15% tak mengisi, semua SIM tak terdaftar, VPN mati, atau jam miring > 60 dtk · KP3 perubahan tampil di dashboard < 3 dtk (SSE) |
| FR-03 | Perintah jarak jauh: ping, refresh SIM, restart service, unggah log, rotasi kredensial | S | KP1 hasil perintah muncul di dashboard · KP2 device offline → 409 |
| FR-04 | Revoke device | M | KP1 WS ditutup 4401 ≤ 5 dtk · KP2 kredensial tidak dapat dipakai lagi · KP3 pesan `queued` tidak hilang · KP4 tercatat di audit |
| FR-05 | Inventaris & pengelolaan SIM (label, enable/disable, policy, counter harian) | M | KP1 SIM baru/dicabut terdeteksi otomatis · KP2 SIM `disabled` tidak menerima pesan baru · KP3 riwayat pesan tetap terhubung ke SIM `absent` |

### 3.2 Pengiriman

| ID | Kebutuhan | Pri | Kriteria penerimaan |
|---|---|---|---|
| FR-06 | Kirim non-campaign dari dashboard (1–1.000 nomor, teks atau template, pilih SIM/device, jadwal, policy) | M | KP1 nomor `08xx` dinormalisasi ke E.164 · KP2 nomor invalid/suppressed dilaporkan per baris, sisanya diproses · KP3 tampil estimasi segmen |
| FR-07 | Kirim via API dengan API key, idempoten | S | KP1 `Idempotency-Key` sama → respons sama, tanpa pesan ganda · KP2 `client_ref` ganda ditolak per penerima · KP3 scope `messages:send` ditegakkan |
| FR-12 | Retry dan cancel pesan | M | KP1 cancel hanya `queued/assigned` · KP2 retry `failed/expired` membuat attempt baru · KP3 retry `unknown` mensyaratkan konfirmasi risiko ganda |

### 3.3 Campaign

| ID | Kebutuhan | Pri | Kriteria penerimaan |
|---|---|---|---|
| FR-08 | Buat campaign: nama, isi/template, penerima (CSV/JSON, ≤ 50.000), SIM, policy | S | KP1 ringkasan impor: diterima/duplikat/suppressed/invalid per baris · KP2 variabel template wajib terpenuhi untuk semua penerima · KP3 preview menampilkan render, segmen, estimasi durasi |
| FR-09 | Kontrol campaign: start, jadwal, pause, resume, cancel | S | KP1 transisi mengikuti state machine (dok. 03 §3 di docs/) · KP2 pause menahan `queued`, menyelesaikan `sending` · KP3 cancel → semua `queued` menjadi `cancelled` |
| FR-10 | Auto-pause saat rasio gagal tinggi / tanpa kapasitas | S | KP1 default 50% pada 50 pesan terakhir · KP2 `pause_reason` terisi · KP3 dapat dinonaktifkan |

### 3.4 Manajemen pengiriman

| ID | Kebutuhan | Pri | Kriteria penerimaan |
|---|---|---|---|
| FR-13 | Policy: delay acak `[min,max]`, burst + jeda, rate/menit-jam-hari, jendela waktu, retry/backoff, TTL, concurrency per SIM | M | KP1 jeda aktual antar kirim pada SIM sama ∈ `[min,max]` ±500 ms · KP2 rate limit tidak pernah terlampaui pada jendela geser · KP3 di luar jendela waktu tak ada pengiriman, lanjut otomatis saat jendela buka |
| FR-14 | Policy berlapis global→device→SIM→campaign/pesan + tampilan efektif | M | KP1 aturan merge (dok. 03 §4) terbukti uji · KP2 irisan jendela kosong ditolak saat simpan · KP3 endpoint `effective` menjelaskan nilai akhir |
| FR-15 | Daftar blokir (suppression) | S | KP1 nomor terblokir tak pernah dikirim (semua jalur) · KP2 impor massal |
| FR-16 | Template berbagi variabel `{{var}}` | S | KP1 variabel terdeteksi otomatis · KP2 hitung encoding & segmen |

### 3.5 Transaksi, akses, operasional

| ID | Kebutuhan | Pri | Kriteria penerimaan |
|---|---|---|---|
| FR-11 | Halaman transaksi: filter (status, sumber, campaign, device, SIM, nomor, tanggal), detail dengan timeline, ekspor CSV | M | KP1 filter gabungan tepat · KP2 timeline memuat semua transisi dengan aktor · KP3 ekspor mengikuti filter · KP4 daftar 1 juta baris tetap responsif (cursor) |
| FR-17 | Pengguna & peran admin/operator/viewer | M | KP1 matriks izin (spec 08 §3) ditegakkan di server · KP2 viewer melihat nomor termasking |
| FR-18 | API key (scope, kedaluwarsa, cabut) | S | KP1 secret tampil sekali · KP2 disimpan ter-hash |
| FR-19 | Dashboard realtime | M | KP1 status pesan/device/campaign terbarui tanpa reload · KP2 reconnect + replay 5 menit |
| FR-20 | Audit log tindakan admin | S | KP1 semua mutasi sensitif tercatat (siapa/apa/kapan) · KP2 tidak ikut retensi pesan |
| FR-21 | Webhook status | C | KP1 HMAC-SHA256 · KP2 retry eksponensial |
| FR-22 | Retensi data | C | KP1 body dihapus setelah N hari · KP2 metadata tetap |
| FR-23 | Autentikasi dua faktor (TOTP) untuk pengguna dashboard | C | KP1 password benar saja tidak memberi sesi · KP2 kode tak dapat dipakai ulang; 5 kegagalan → kunci · KP3 kode pemulihan sekali pakai; reset darurat oleh admin/CLI |
| FR-24 | Rotasi kredensial tanpa downtime (device, API key, secret webhook, kunci server) | C | KP1 masa tumpang tindih: secret lama & baru sama-sama sah · KP2 rotasi device tidak dapat mengunci HP · KP3 secret baru tak pernah muncul di log/audit |
| FR-25 | Kewajiban 2FA yang dapat dinyalakan admin (default nonaktif) | C | KP1 default `off` · KP2 akun dalam cakupan tanpa 2FA hanya mencapai endpoint akun, termasuk sesi yang sudah ada · KP3 admin tanpa 2FA tidak bisa menyalakannya · KP4 API key tidak terpengaruh |
| FR-26 | Manajemen pengguna: ubah peran, nonaktifkan, admin menetapkan password | C | KP1 peran berlaku seketika · KP2 nonaktif mencabut sesi & menolak login · KP3 tidak bisa mengubah diri sendiri · KP4 semuanya diaudit |
| FR-27 | Undangan pengguna: tautan sekali pakai, penerima menetapkan password sendiri | C | KP1 token tampil sekali, hanya hash disimpan · KP2 sekali pakai (aman terhadap penerimaan paralel) · KP3 kedaluwarsa/dicabut/digantikan → ditolak seragam · KP4 password pendek/email bentrok tidak menghabiskan undangan |
| FR-28 | Hapus pengguna (harus dinonaktifkan dahulu) | C | KP1 sesi hilang, objek buatannya tetap ada · KP2 audit tetap memuat email pelaku · KP3 akun sendiri ditolak |
| FR-30 | Pembersihan undangan lama (retensi dapat diatur) | C | KP1 hanya undangan selesai (diterima/dicabut/kedaluwarsa) yang lewat masa simpan · KP2 undangan tertunda & pengguna hasilnya tidak tersentuh · KP3 idempoten |
| FR-31 | Pengiriman email undangan lewat SMTP (opsional) | C | KP1 tanpa SMTP undangan tetap berjalan (tautan manual) · KP2 outbox + retry, kegagalan sementara diulang, penolakan permanen tidak · KP3 isi email terenkripsi dan dibuang setelah selesai/dibatalkan · KP4 tidak pernah turun ke koneksi tanpa enkripsi bila starttls/tls diminta |
| FR-32 | WebAuthn (kunci keamanan / passkey) sebagai faktor kedua | C | KP1 assertion/attestation diverifikasi penuh (challenge, origin, RP ID, tanda tangan, sign count) · KP2 upacara sekali pakai & terikat pengguna · KP3 kegagalan dihitung ke lockout; sign count mundur ditolak & diaudit · KP4 dihitung sebagai 2FA oleh kebijakan wajib-2FA · KP5 kode pemulihan diterbitkan sekali saat faktor pertama |
| FR-29 | Kebijakan password baru (daftar password umum, pola, kemiripan email) | C | KP1 berlaku di semua pintu masuk · KP2 tidak retroaktif · KP3 penolakan menyebut alasan · KP4 undangan tidak hangus bila password ditolak |

## 4. Kebutuhan non-fungsional

| ID | Kebutuhan | Target terukur |
|---|---|---|
| NFR-01 | Standby device | HP idle layar mati ≥ 24 jam tetap `online`; uptime koneksi ≥ 99% pada tiga merek HP uji |
| NFR-02 | Latensi perintah | `lease → HP mulai kirim` ≤ 5 dtk (P95) pada koneksi sehat |
| NFR-03 | Tanpa SMS ganda | 0 duplikat pada uji kegagalan (dok. 12 §5); kasus tak pasti → `unknown`, bukan kirim ulang |
| NFR-04 | Kemudahan setup | Enrollment dari nol ≤ 3 menit, ≤ 4 interaksi di HP |
| NFR-05 | Throughput | Server mampu menjadwalkan ≥ 50 pesan/dtk agregat; throughput nyata dibatasi policy/SIM |
| NFR-06 | Keamanan | Tidak ada endpoint publik pada Profil A; semua secret ter-hash; lolos checklist dok. 09 |
| NFR-07 | Ketahanan data | Restart server di tengah pengiriman tidak menghilangkan/menggandakan pesan; RPO ≤ 24 jam (backup harian), WAL archiving opsional |
| NFR-08 | Kompatibilitas | Android 10 (API 29) – terbaru; uji di API 29, 31, 33, 34, 35+ |
| NFR-09 | Observabilitas | Metrik, log terstruktur, health endpoint, alert dasar (dok. 10 §6) |
| NFR-10 | Privasi | Body tidak di log; masking viewer; retensi dapat dikonfigurasi |

## 5. Batasan dan asumsi

- Distribusi sideload/MDM; HP didedikasikan dan terus tercolok daya.
- Satu organisasi; satu server (tanpa HA) pada v1.
- Throughput nyata mengikuti operator/OS (lihat risiko R1 di `docs/05`).
- Kepatuhan regulasi (izin pengirim, consent, UU PDP) menjadi tanggung jawab pemilik; sistem menyediakan alatnya (suppression, masking, retensi).

## 6. Skenario pengguna utama

1. **Siapkan HP baru** (Teknisi): login Tailscale → pasang APK → checklist → scan QR → HP muncul online dengan 2 SIM.
2. **Kirim cepat** (Operator): tempel 3 nomor, ketik pesan, pilih SIM 2, kirim → pantau status `queued → sent → delivered`.
3. **Campaign 5.000 penerima** (Operator): unggah CSV, pilih template, atur delay 5–12 dtk dan jendela 09–18, preview, start → pantau progres; saat SIM 1 kehilangan sinyal, campaign auto-pause; operator memperbaiki lalu resume.
4. **Integrasi** (Integrator): sistem billing memanggil `POST /messages` dengan `Idempotency-Key`; membaca status via `GET /messages?client_ref=`.
5. **HP hilang** (Admin): revoke → HP terputus dan terkunci; pesan antre dialihkan ke SIM lain.

## 7. Glosarium

| Istilah | Arti |
|---|---|
| Transaksi / Message | Satu SMS ke satu penerima (dapat multi-part), dengan siklus status |
| Lane | Jalur pengiriman logis per SIM yang dikendalikan dispatcher |
| Lease | Pemberian sementara sebuah pesan ke satu SIM/device sampai di-ack |
| Guardrail | Batas policy tingkat global/device/SIM yang tidak bisa dilonggarkan campaign |
| Outbox | Antrean event lokal di HP yang dikirim sampai di-ack server |
| Unknown | Status pesan yang hasil akhirnya tak dapat dipastikan (device hilang) |
