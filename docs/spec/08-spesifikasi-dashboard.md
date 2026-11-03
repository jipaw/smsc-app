# Spec 08 — Spesifikasi Dashboard

React + TypeScript + Vite, TanStack Query/Router/Table, Tailwind + shadcn/ui, Recharts. Tipe API dihasilkan dari `contracts/openapi.yaml`. Bahasa UI: Indonesia (struktur i18n siap bilingual).

## 1. Struktur navigasi

```
Overview
Devices ── detail ── (SIM, perintah, policy)
SIM
Campaigns ── wizard ── detail
Kirim SMS
Transaksi ── detail (drawer)
Template
Pengaturan ── Policy · Daftar blokir · API key · Pengguna · Webhook · Retensi · Keamanan · SSO · SCIM · Email · Audit
Akun (tautan email di header) ── Ganti password · 2FA
```

Layout: sidebar kiri (ikon + label, tersembunyi di bawah 768 px menjadi drawer), header dengan indikator koneksi SSE (hijau/kuning/merah), pencarian global nomor/ID, menu pengguna. Peringatan kritis (semua device offline, SIM bermasalah) ditampilkan sebagai banner di atas konten.

## 2. Pola umum

| Aspek | Aturan |
|---|---|
| Data | TanStack Query; `staleTime` 5 dtk; invalidasi dari SSE (`message.updated` → patch cache baris terkait, tidak refetch penuh) |
| Realtime | Satu `EventSource` global; pada `resync` atau reconnect lama → invalidasi query aktif. Banner "Koneksi realtime terputus" + tombol segarkan |
| Status | Lencana berwarna **dan** berikon/teks (tidak hanya warna): `queued` abu, `assigned/sending` biru, `sent` hijau muda, `delivered` hijau, `failed` merah, `cancelled` abu gelap, `expired` oranye, `unknown` ungu dengan ikon tanya |
| Kondisi | Setiap halaman wajib memiliki state *loading* (skeleton), *kosong* (CTA), *error* (pesan + coba lagi + `X-Request-Id`) |
| Waktu | Ditampilkan zona browser dengan tooltip UTC; relatif untuk < 24 jam |
| Nomor | Teks monospace; peran viewer termasking `+62812****7890` (dikirim sudah termasking oleh server) |
| Aksi destruktif | Dialog konfirmasi menyebut dampak (jumlah pesan terdampak); ketik nama untuk revoke device/hapus |
| Aksesibilitas | WCAG 2.1 AA: fokus terlihat, navigasi keyboard penuh pada tabel/dialog, label ARIA, kontras ≥ 4.5:1, tidak mengandalkan warna saja, `prefers-reduced-motion` |
| Responsif | Desktop-first; tabel menjadi kartu < 768 px untuk Transaksi dan Devices |
| Tema | Terang/gelap mengikuti sistem |

## 3. Matriks peran (ditegakkan di server; UI menyembunyikan aksi)

| Aksi | admin | operator | viewer |
|---|---|---|---|
| Lihat Overview, Devices, SIM, Transaksi, Campaign, Template | ✓ | ✓ | ✓ (masking) |
| Tambah/ubah/revoke device, ubah SIM, perintah device | ✓ | – | – |
| Kirim SMS, retry/cancel | ✓ | ✓ | – |
| Buat/ubah/kontrol campaign, kelola template | ✓ | ✓ | – |
| Ekspor CSV | ✓ | ✓ | – |
| Policy, daftar blokir, API key, pengguna, webhook, audit | ✓ | – | – |

## 4. Halaman

### 4.1 Overview

- Kartu: Device online/degraded/offline, SIM aktif, Antrean (`queue_depth`), Terkirim hari ini, Gagal hari ini, Success rate 24 jam.
- Grafik batang bertumpuk per jam (sent/delivered/failed), rentang 24 jam/7 hari.
- Panel "Perlu perhatian": daftar device `degraded/offline` dengan alasan, SIM `no_service`, campaign `paused` (auto), pesan `unknown`.
- Data: `/stats/overview`, `/stats/timeseries`; refresh lewat SSE dan interval 30 dtk.

### 4.2 Devices

- **Daftar**: nama, status (lencana), baterai/charging, jaringan, versi app, SIM (chip dengan operator + sinyal), terakhir terlihat. Filter status.
- **Tambah device** (dialog): input nama + policy opsional → tampil QR besar, kode manual `XXXX-XXXX`, hitung mundur 15 mnt, tombol "Salin tautan", "Buat ulang". Menunggu enrollment: dialog berubah otomatis menjadi "Device terhubung ✓" saat `device.updated` pertama tiba.
- **Detail**: kartu info (model, Android, app, IP tailnet, capability dengan ikon centang/peringatan dan tautan panduan jika false), tab SIM, tab Riwayat (pesan terakhir), tab Aktivitas (event status & perintah), tombol perintah (Ping, Refresh SIM, Restart service, Ambil log, Rotasi kredensial), pilih policy, toggle enabled, **Revoke** (merah, konfirmasi).
- Peringatan capability: `os_sms_limit_unlocked=false` → info "Batas OS aktif: maks 25 pesan/30 mnt/HP".

### 4.3 SIM

Tabel lintas device: Device, Slot, Label (inline edit), Operator, Nomor, Sinyal, Status, Enabled (toggle), Policy, Hari ini (sent/delivered/failed), Berikutnya (`next_eligible_at` hitung mundur). Aksi massal: aktif/nonaktifkan. Klik baris → panel dengan grafik 24 jam dan policy efektif (memanggil `.../effective`).

### 4.4 Kirim SMS

Formulir satu halaman:

1. **Penerima**: area teks (satu nomor per baris/koma) atau unggah file; validasi langsung (jumlah valid/invalid, duplikat), maks 1.000.
2. **Pesan**: tab "Tulis" / "Template"; penghitung karakter/encoding/segmen realtime (algoritma sama dengan server); pratinjau untuk penerima pertama bila ada variabel.
3. **Rute** (collapse, default "Semua SIM aktif"): pilih device/SIM.
4. **Waktu & policy** (collapse): Kirim sekarang / Jadwalkan; policy; prioritas; TTL.
5. **Ringkasan**: "N pesan · M segmen · estimasi selesai ≈ …" → tombol **Kirim**. Setelah sukses: toast + tautan ke Transaksi terfilter (`ids` dari respons); baris `rejected` ditampilkan dengan alasan.

### 4.5 Transaksi

- **Filter bar**: status (multi), sumber, campaign, device, SIM, rentang tanggal, pencarian nomor/ID/`client_ref`. Filter tersimpan di URL (dapat dibagikan).
- **Tabel** (virtualisasi, cursor infinite): waktu, nomor, isi (dipotong 60 char, tooltip), sumber/campaign, device·SIM, status, attempt, error. Baris baru/berubah disorot 2 dtk. Toggle "Live" (default aktif; berhenti bila pengguna menggulir ke bawah, tombol "Muat N baru").
- **Aksi massal**: Retry (hanya `failed/expired`), Cancel (hanya `queued/assigned`) — pratinjau jumlah yang eligible; `unknown` hanya individual dengan dialog risiko.
- **Drawer detail**: ringkasan (nomor, isi lengkap, segmen/encoding, device/SIM, waktu penting), **timeline** vertikal (jenis event, aktor, waktu absolut + selisih dari event sebelumnya, `error_code`/`android_result_code`), tombol Retry/Cancel, salin ID.
- **Ekspor**: tombol → `GET /messages/export` dengan filter aktif; peringatan bila > 100.000 baris (diproses stream).

### 4.6 Campaigns

- **Daftar**: nama, status, progres (bar bertumpuk sent/delivered/failed/queued), jadwal, dibuat oleh.
- **Wizard 5 langkah** (draft otomatis tersimpan; dapat kembali):
  1. *Detail*: nama, deskripsi.
  2. *Penerima*: unggah CSV/tempel; tabel hasil validasi (diterima/duplikat/suppressed/invalid, unduh baris invalid), pemetaan kolom → variabel.
  3. *Pesan*: tulis atau pilih template; variabel yang tidak ada di kolom CSV ditandai merah (blok lanjut). Saat "Lanjut", pesan disimpan lalu **seluruh penerima diperiksa** (isi pesan baru diketahui di langkah ini, jadi sel kosong pada baris tertentu baru ketahuan di sini): bila ada, wizard tertahan dengan jumlah per variabel dan tombol kembali ke langkah Penerima untuk hapus-semua + impor ulang.
  4. *SIM & Policy*: pilih SIM (default semua), policy (dropdown + ringkasan efektif: delay, rate, jendela, estimasi durasi), auto-pause (toggle + ambang), prioritas.
  5. *Review & jadwal*: ringkasan semua, tombol **Mulai sekarang** / **Jadwalkan** (pilih waktu).
- **Detail**: header status + tombol (Pause/Resume/Cancel), kartu counter, grafik progres, estimasi selesai, banner `paused` dengan `pause_reason` dan saran tindakan, tab Pesan (tabel Transaksi terfilter), tab Pengaturan (ubah policy/SIM saat berjalan).

### 4.7 Template

Daftar + editor: nama, isi, daftar variabel terdeteksi, penghitung segmen, tombol "Gunakan di campaign/kirim".

### 4.8 Pengaturan

- **Policy**: daftar dengan lencana *Default*; editor berbagi: Delay (min/max dengan slider + input ms), Burst, Rate (menit/jam/hari), Jendela waktu (grid hari × jam + zona), Retry (max attempts, backoff), TTL, tracking delivery, timeout, `retry_on_unknown`. Field kosong = "warisi". Panel kanan "Simulasi": estimasi throughput per SIM dan contoh jadwal 10 pesan. Validasi inline (`min ≤ max`, irisan jendela).
- **Daftar blokir**: tabel + tambah/impor + hapus.
- **API key**: buat (nama, scope, kedaluwarsa) → secret tampil sekali dengan tombol salin; **Putar** (pilih masa tumpang tindih; kolom Rotasi menunjukkan sampai kapan secret lama sah); cabut.
- **Pengguna**: tambah; ubah **peran** dari tabel (berlaku seketika); **Nonaktifkan/Aktifkan**; **Set password** (admin menetapkan, sesi pengguna dicabut); kolom **2FA** dan *Reset 2FA*. Baris akun sendiri tanpa kontrol ubah (server menolak, `user.self_change`).
- **Undang pengguna** (di tab Pengguna): email + peran + masa berlaku → tautan tampil sekali (salin); daftar undangan (menunggu/diterima/kedaluwarsa/dicabut) dengan *Cabut*. Halaman publik `/invite/<token>`: tampilkan email & peran, penerima mengisi password (min. 12, diulang) → akun dibuat → ke halaman masuk. Token tidak valid → pesan seragam.
- **Email undangan**: kotak centang *Kirim tautan ke email penerima* (nonaktif + keterangan bila SMTP belum dikonfigurasi); kolom *Email* (antre/terkirim/gagal/dibatalkan, tooltip alasan gagal; daftar menyegar sendiri selagi ada yang antre); *Kirim ulang* membuat undangan baru dan mengirimnya. Tab **Email**: ringkasan SMTP (tanpa kredensial), peringatan bila tanpa enkripsi, email uji.
- **Hapus** (baris pengguna): hanya muncul untuk akun nonaktif, dengan konfirmasi.
- **Keamanan (lanjutan)**: kotak centang *Izinkan login tanpa password dengan passkey* (default mati) dengan peringatan soal passkey tersinkron.
- **Keamanan**: kebijakan 2FA — Opsional (default) / Wajib untuk admin / Wajib untuk semua, dengan hitungan pengguna dalam cakupan yang belum memakai 2FA. Akun yang digerbang melihat shell minimal berisi halaman Akun + banner; dashboard terbuka tanpa login ulang begitu 2FA aktif dan kode pemulihan sudah disimpan.
- **Webhook**: buat/uji/riwayat/kirim ulang; **Putar secret** dengan masa tumpang tindih.
- **Webhook** (fase 3), **Audit log** (filter aktor/aksi/tanggal, read-only).

### 4.9 Akun (semua peran)

- **Kunci keamanan & passkey**: daftar (nama, ditambahkan, terakhir dipakai, lencana *passkey tersinkron*), *Tambah kunci* (nama + password → dialog browser), *Hapus* (password + kode TOTP/pemulihan). Peringatan bila WebAuthn tak tersedia (RP ID IP / browser tak mendukung / bukan HTTPS). Kunci pertama menampilkan kode pemulihan sekali.
- **SCIM** (Pengaturan > SCIM, admin): status dan SCIM base URL untuk IdP, opsi aktif (peran bawaan, izin admin, adopsi), buat token (tampil sekali) / daftar / cabut. Daftar pengguna menandai akun yang dikelola SCIM.
- **SSO**: halaman login menampilkan *Masuk dengan {nama IdP}* bila dikonfigurasi dan pesan galat ringkas dari `?sso_error`. Halaman Akun: bagian *Single sign-on* (Tertaut / Hubungkan akun SSO / Lepas tautan); akun SSO-saja tidak melihat form password/2FA. Pengaturan > **SSO** (admin): penyedia, Client ID, redirect URI yang harus didaftarkan, opsi aktif, jumlah tertaut, *Uji koneksi ke IdP*. Daftar pengguna: lencana *SSO* dan *tanpa password*.
- **Passkey**: kotak centang *Daftarkan sebagai passkey* di formulir Tambah kunci (hanya muncul bila admin mengizinkan); lencana *passkey* pada daftar. **Halaman login**: tombol *Masuk dengan passkey* (hanya bila diizinkan admin dan browser mendukung) di bawah formulir password.
- **Login langkah 2**: tombol *Gunakan kunci keamanan* bila akun punya kunci; di bawahnya kolom kode TOTP/pemulihan.


- **Ganti password**: password saat ini + baru (≥ 12, diulang). Sesi lain dikeluarkan.
- **Verifikasi 2 langkah**: Siapkan (minta password) → QR + kunci manual → kode 6 digit untuk mengaktifkan → 10 kode pemulihan tampil **sekali** (salin/unduh). Saat aktif: tanggal, sisa kode pemulihan (peringatan ≤ 2), *Terbitkan ulang kode*, *Nonaktifkan* (butuh password + kode).
- **Login**: bila 2FA aktif, setelah password muncul langkah kedua (kode TOTP atau kode pemulihan); Kembali membatalkan.

## 5. Wireframe (ASCII)

**Transaksi**

```text
┌ Transaksi ──────────────────────────────────────────────── ● Live ┐
│ [Status ▾ failed,unknown] [Sumber ▾] [Campaign ▾] [SIM ▾] [Tgl ▾] [🔍 nomor/ID]
│ Ditemukan 128 · Retry (12) · Cancel (0) · Ekspor CSV
├──────┬─────────────┬──────────────┬──────────────┬─────────┬───────┬────────────┤
│ ☐    │ Waktu       │ Nomor        │ Isi          │ SIM     │ Status│ Error      │
│ ☐    │ 10:15:33    │ +62812…7890  │ Halo Budi,…  │ HP1·S1  │ ✗ failed │ no_service │
│ ☐    │ 10:15:20    │ +62813…1122  │ Kode OTP …   │ HP1·S2  │ ✓ delivered │         │
└──────┴─────────────┴──────────────┴──────────────┴─────────┴───────┴────────────┘
```

**Tambah device**

```text
┌ Tambah device ─────────────────────────────┐
│ Nama [HP Gateway 1______]   Policy [Default ▾]
│        ┌──────────┐   Kode manual: K7QX-92MD
│        │  [ QR ]  │   Berlaku 14:32  ⟳ Buat ulang
│        └──────────┘   1 Login Tailscale  2 Pasang app  3 Beri izin  4 Scan QR
│ ○ Menunggu perangkat…
└─────────────────────────────────────────────┘
```

## 6. Aturan validasi sisi klien (cermin server)

| Field | Aturan |
|---|---|
| Nomor | Normalisasi E.164 (`libphonenumber-js`, region ID); tampilkan hasil normalisasi |
| Body | ≤ 1.600 karakter; hitung encoding GSM-7/UCS-2 & segmen (aturan spec 04 §9) |
| Delay | `min ≤ max`; peringatan jika `min < 1000` ms ("risiko diblokir operator") |
| Jendela waktu | Mulai < selesai; irisan kosong ditandai |
| Campaign | Penerima ≥ 1; variabel template ⊂ kolom CSV |

Server tetap sumber validasi; klien menampilkan `errors[]` per field dari `validation.failed`.

## 7. Performa dan keamanan UI

- Bundel awal ≤ 300 KB gzip (code-splitting per rute; Recharts dimuat lazy).
- Tabel Transaksi: virtualisasi, halaman 100 baris per fetch.
- CSP ketat (`default-src 'self'`; tanpa inline script), tidak ada penyimpanan token di `localStorage` (cookie HttpOnly), CSRF header otomatis di klien HTTP.
- Tidak ada isi SMS di URL/logging analitik.
