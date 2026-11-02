# Spec 11 — Panduan Pengguna

Untuk Teknisi HP, Operator, dan Admin. Istilah status ada di §8.

## 1. Menyiapkan HP gateway (Teknisi)

Waktu ±3 menit. Siapkan: HP Android 10+, SIM aktif (1 atau 2), koneksi internet, akses ke dashboard.

**Sebelum mulai (sekali per HP):** pasang kartu SIM, pastikan SIM dapat kirim SMS manual, hubungkan ke daya.

1. **Login Tailscale.** Pasang *Tailscale* dari toko aplikasi, masuk dengan akun organisasi (atau kunci yang diberikan Admin). Pastikan status *Connected* dan aktifkan *Always-on VPN* bila diminta.
2. **Pasang aplikasi SMSC.** Buka berkas APK, izinkan "instal dari sumber ini".
3. **Buka aplikasi dan ikuti checklist.** Satu layar menampilkan item yang harus hijau:
   - ✓ Tailscale tersambung ke server
   - ✓ Izin SMS dan Telepon
   - ✓ Notifikasi
   - ✓ Abaikan optimasi baterai (ikuti panduan merek HP Anda)

   > Jika izin SMS tidak bisa dicentang (abu-abu): *Pengaturan → Aplikasi → SMSC → ⋮ → Izinkan setelan terbatas*, lalu kembali ke app dan ulangi.
4. **Scan QR.** Di dashboard buka *Devices → Tambah device*, scan QR dengan app (atau ketik kode `XXXX-XXXX`). QR berlaku 15 menit dan hanya sekali pakai.
5. **Selesai.** App menampilkan **Online ✓** dan dashboard menampilkan HP dengan SIM-nya.

Tips agar HP selalu siaga: tetap dicolok daya, matikan mode hemat baterai/adaptive battery, kunci aplikasi di recent apps bila tersedia, aktifkan *auto-start*, dan lakukan juga untuk aplikasi **Tailscale**.

## 2. Mengelola device dan SIM (Admin)

- **Devices**: lihat status, baterai, jaringan, versi app. Klik device untuk detail.
- **Beri nama/label** device dan SIM (mis. "HP Kantor – Telkomsel").
- **Nonaktifkan SIM** sementara: toggle *Enabled* di halaman SIM — pesan baru tidak akan memakai SIM itu.
- **Perintah**: *Ping* (tes), *Refresh SIM* (setelah ganti SIM), *Restart service*, *Ambil log* (untuk bantuan teknis; tidak berisi isi SMS).
- **Revoke**: gunakan jika HP hilang/diganti. HP langsung terputus dan harus didaftarkan ulang. Pesan antre tidak hilang.

## 3. Kirim SMS cepat (Operator)

1. Menu **Kirim SMS**.
2. Isi **Penerima** (satu nomor per baris, maks 1.000). Nomor `0812…` otomatis menjadi `+62812…`. Nomor tidak valid/terblokir ditandai.
3. Tulis **Pesan** atau pilih **Template**. Perhatikan penghitung: pesan > 160 karakter (atau > 70 jika ada karakter khusus/emoji) dikirim sebagai beberapa segmen.
4. (Opsional) pilih SIM tertentu, jadwal, prioritas.
5. Klik **Kirim**. Pantau di **Transaksi**.

## 4. Membuat campaign (Operator)

Siapkan file CSV (UTF-8). Kolom `phone` wajib; kolom lain dapat dipakai sebagai variabel pesan.

```csv
phone,name,amount,due
08123456789,Budi,150000,5 Nov
08129876543,Sari,90000,6 Nov
```

Pesan: `Halo {{name}}, tagihan Rp{{amount}} jatuh tempo {{due}}.`

1. **Campaigns → Buat campaign.**
2. **Penerima**: unggah CSV. Perbaiki baris invalid (unduh daftar), duplikat dibuang otomatis, nomor terblokir dilewati.
3. **Pesan**: tulis/ pilih template; semua `{{variabel}}` harus ada di kolom CSV. Lihat preview.
4. **SIM & Policy**: pilih SIM dan policy pengiriman (lihat §6). Biarkan *Auto-pause* aktif.
5. **Review** → **Mulai** atau **Jadwalkan**.
6. Pantau progres. Kontrol: **Pause** (menahan sisa antrean), **Resume**, **Cancel** (membatalkan yang belum terkirim).

Bila campaign berhenti sendiri (*Paused – auto*), baca alasan di banner (biasanya banyak kegagalan karena sinyal/pulsa), perbaiki, lalu **Resume**.

## 5. Membaca Transaksi

- Filter berdasarkan status, campaign, SIM, tanggal, atau cari nomor/ID.
- Klik baris untuk **timeline**: kapan diantrekan, kapan diberikan ke HP, kapan terkirim, kapan laporan sampai, serta alasan jika gagal.
- **Retry** untuk `failed/expired`; **Cancel** untuk yang masih antre.
- **Ekspor CSV** mengikuti filter yang aktif.

## 6. Mengatur kecepatan dan keamanan pengiriman (Admin)

Pengaturan → **Policy pengiriman**. Pengiriman terlalu cepat dapat membuat nomor diblokir operator, jadi mulai konservatif.

| Parameter | Fungsi | Saran awal |
|---|---|---|
| Jeda min–max | Jeda acak antar SMS pada SIM yang sama | 3–8 dtk (OTP/transaksional: 1–3 dtk; promosi massal: 8–20 dtk) |
| Burst & jeda burst | Setelah N SMS berturut-turut, istirahat | 20 SMS lalu 60 dtk |
| Batas per menit/jam/hari | Plafon per SIM | 10 / 300 / 2.000 |
| Jendela waktu | Jam boleh kirim | 08:00–20:00 |
| Percobaan ulang | Jumlah & jeda retry saat gagal sementara | 3×, mulai 30 dtk |
| Masa berlaku (TTL) | Batas waktu pesan menunggu | 24 jam |

Hierarki: **Global → Device → SIM** (batas aman, yang paling ketat menang) lalu **Campaign/Pesan** (preferensi, tidak bisa melonggarkan batas aman). Gunakan **Policy efektif** pada halaman SIM untuk melihat nilai akhir.

Catatan: Android dapat membatasi sekitar 25–30 SMS per 30 menit per HP kecuali dikonfigurasi khusus; lihat badge *Batas OS aktif* pada detail device.

## 7. Integrasi API (Integrator)

1. Admin membuat **API key** (Pengaturan → API key), pilih scope `messages:send`/`messages:read`; salin secret (hanya tampil sekali).
2. Kirim `POST /api/v1/messages` dengan header `Authorization: Bearer <key>` dan `Idempotency-Key` unik per transaksi bisnis (contoh di spec 05).
3. Simpan `id` pesan dan `client_ref`; cek status dengan `GET /messages/{id}`.
4. Anggap `delivered` sebagai konfirmasi sampai; `sent` berarti sudah diserahkan ke jaringan (operator tertentu tidak mengirim laporan sampai).

## 8. Arti status

| Status | Arti | Tindakan |
|---|---|---|
| `queued` | Menunggu giliran (jeda/jadwal/antrean) | Tunggu; Cancel jika perlu |
| `assigned` | Sudah diberikan ke HP, menunggu konfirmasi | Otomatis |
| `sending` | HP sedang mengirim | Otomatis |
| `sent` | Diserahkan ke jaringan operator | Menunggu laporan; bisa final |
| `delivered` | Sampai ke penerima (laporan operator) | – |
| `failed` | Gagal; lihat alasan | Perbaiki lalu Retry |
| `cancelled` | Dibatalkan pengguna | – |
| `expired` | Melewati masa berlaku sebelum terkirim | Retry bila masih perlu |
| `unknown` | Hasil tidak pasti (HP terputus saat mengirim) | Tunggu HP kembali; baca runbook R-04; Retry hanya bila yakin belum terkirim |

Alasan gagal umum: `no_service` (tidak ada sinyal), `radio_off` (mode pesawat), `generic_failure` (operator/pulsa), `os_rate_limited` (batas Android), `permission_denied` (izin SMS dicabut), `invalid_destination` (nomor salah).

## 9. Pemecahan masalah cepat

| Gejala | Kemungkinan penyebab | Solusi |
|---|---|---|
| QR tidak bisa dipakai | Kedaluwarsa/terpakai | Buat QR baru |
| Wizard: Tailscale merah | VPN mati / server tak terjangkau | Buka Tailscale, sambungkan; cek ACL ke Admin |
| Izin SMS abu-abu | Setelan terbatas (sideload) | Izinkan setelan terbatas di Info Aplikasi |
| Device sering offline | Hemat baterai/OEM menutup app | Terapkan panduan merek; cek Tailscale juga dikecualikan |
| Pesan tertahan di `queued` | Di luar jendela waktu, batas rate, SIM nonaktif | Lihat runbook R-02 |
| Banyak `failed no_service` | Sinyal buruk | Pindahkan HP, cek SIM |
| Campaign berhenti sendiri | Auto-pause | Baca banner, perbaiki, Resume |
| Notifikasi hilang | Izin notifikasi dicabut | Aktifkan; service tetap jalan namun disarankan terlihat |

## 10. Etika dan kepatuhan

Kirim hanya kepada penerima yang telah menyetujui, sertakan cara berhenti berlangganan bila sifatnya promosi, dan tambahkan penolakan ke **Daftar blokir**. Ikuti ketentuan operator dan hukum setempat.
