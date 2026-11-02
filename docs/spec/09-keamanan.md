# Spec 09 — Keamanan

Melengkapi `docs/04` §2–3. Mengikat untuk implementasi; checklist di §9 menjadi gerbang rilis.

## 1. Aset dan batas kepercayaan

| Aset | Sensitivitas | Lokasi |
|---|---|---|
| Nomor penerima & isi SMS | Tinggi (data pribadi) | `messages`, buffer sementara di HP |
| `device_secret` | Kritis | HP (Keystore), hash di DB |
| API key / password / sesi | Kritis | Hash di DB |
| Kemampuan mengirim SMS dari SIM | Kritis (biaya, reputasi, penyalahgunaan) | Server → HP |
| Policy & konfigurasi | Sedang | DB |
| Audit log | Tinggi (integritas) | DB |

Batas: browser↔server, sistem eksternal↔server, HP↔server, server↔DB, dan HP↔jaringan seluler (di luar kendali).

## 2. Model ancaman (STRIDE)

| # | Ancaman | Kategori | Kontrol |
|---|---|---|---|
| T1 | Penyerang di jaringan menyamar sebagai HP | Spoofing | Bearer `device_secret` 256-bit; WSS; Profil A: ACL tailnet; opsional mTLS/pinning |
| T2 | Pencurian QR enrollment | Spoofing | TTL 15 mnt, sekali pakai, dapat dicabut, kode manual dibatasi 5 percobaan |
| T3 | HP dicuri lalu dipakai kirim SMS | Elevation | Revoke segera; guardrail lokal membatasi laju; panduan blokir SIM |
| T4 | Sesi admin dibajak / CSRF | Spoofing/Tampering | Cookie HttpOnly+Secure+SameSite=Lax, CSRF header, sesi pendek, lockout login |
| T5 | API key bocor → spam | Elevation | Scope, kedaluwarsa, rate limit, suppression, auto-pause, audit, hanya dari tailnet |
| T6 | Injeksi (SQL, template, CSV/formula) | Tampering | sqlc parametrik; template tanpa ekspresi; ekspor CSV meng-escape sel berawalan `= + - @` |
| T7 | SMS ganda akibat replay | Tampering | `message_id`+`attempt`, dedup device, `event_id` idempoten |
| T8 | Kebocoran PII via log/backup | Info disclosure | Log tanpa body, nomor dimasking, retensi body, backup terenkripsi, hash kredensial |
| T9 | Admin menyangkal tindakan | Repudiation | Audit log append-only mencatat aktor & target |
| T10 | DoS: banjir koneksi/pesan | DoS | Rate limit WS 50 msg/dtk, ukuran frame 64 KiB, batas koneksi, ACL tailnet |
| T11 | Server dikompromikan → perintah berbahaya ke HP | Elevation | HP hanya menerima enum perintah terbatas; tidak ada eksekusi kode/URL jarak jauh; guardrail lokal |
| T12 | Aplikasi di HP dibongkar (rooted) | Info disclosure | Secret AEAD di Keystore (hardware-backed bila ada); revoke bila dicurigai; tidak ada data lain bernilai di HP |
| T13 | Penyalahgunaan header identitas Tailscale | Spoofing | Hanya dipercaya dari loopback/sidecar terkonfigurasi |
| T14 | Enumerasi akun | Info disclosure | Pesan login seragam, waktu respons konstan (hash dummy) |

## 3. Kriptografi dan penyimpanan kredensial

| Item | Pilihan |
|---|---|
| Transport | TLS 1.2+ (WSS/HTTPS). Profil A: dilayani Tailscale Serve; Profil B: reverse proxy dengan HSTS |
| Password | **argon2id** m=64 MiB, t=3, p=1 (disetel ≤ 250 ms di server target), salt 16 B, format PHC |
| Token acak (enroll, sesi, API key, device secret) | 256-bit dari CSPRNG; disimpan SHA-256 (cukup karena entropi tinggi); perbandingan `subtle.ConstantTimeCompare` |
| Kode manual enroll | 8 karakter alfabet tanpa karakter ambigu (≈ 40 bit) + batas percobaan 5/token + kedaluwarsa pendek |
| Secret di HP | AES-256-GCM dengan kunci Android Keystore (non-exportable; `setIsStrongBoxBacked` bila tersedia), ciphertext di DataStore |
| Signing webhook | HMAC-SHA256 + timestamp, secret per webhook |
| Cookie sesi | 256-bit, hash di DB, rotasi saat login |

## 4. Kontrol akses

- Otorisasi di server pada setiap handler (matriks spec 08 §3); UI hanya kemudahan.
- Prinsip hak minimal: API key berscope; device hanya `/device/*` dan `/ws/device`; viewer tanpa PII penuh.
- Perubahan peran/pengguna, policy, API key, revoke device, ekspor → audit (aktor, IP/tailnet identity, sebelum/sesudah ringkas).
- Akun admin pertama dibuat via CLI; kebijakan password min 12 karakter + pengecekan daftar password umum; 2FA TOTP disarankan di fase 3.

## 5. Keamanan jaringan

- Profil A: ACL default-deny; hanya `tag:smsc-device` dan `group:smsc-admins` → `tag:smsc-server:443`. Server membind loopback; tidak ada listener publik. Key expiry dimatikan untuk node ber-tag, ditinjau berkala.
- Profil B: dua hostname terpisah (device vs admin), WAF/rate limit, hanya rute device terbuka; opsi mTLS.
- Header keamanan dashboard: `Content-Security-Policy: default-src 'self'; frame-ancestors 'none'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Strict-Transport-Security`, `Cache-Control: no-store` untuk API.
- CORS: dinonaktifkan (dashboard satu origin); API eksternal tidak memakai cookie.

## 6. Perlindungan data pribadi

- Prinsip minimisasi: simpan isi SMS hanya selama perlu (retensi body default 90 hari; dapat 0 untuk "hapus segera setelah terminal").
- Masking pada peran viewer, log, dan di HP.
- Hak subjek data (akses/hapus): prosedur hapus per nomor (hapus/anonimkan `to_e164`, `body`, `vars`, tambah ke suppression bila diminta berhenti) disediakan sebagai perintah CLI `smscd privacy erase --number …` (fase 3).
- Kepatuhan hukum (mis. UU Pelindungan Data Pribadi No. 27/2022, ketentuan operator/Kominfo tentang SMS komersial) adalah tanggung jawab pemilik sistem; dokumen ini tidak menggantikan nasihat hukum. Sistem menyediakan alat teknis: suppression, consent record opsional (`client_ref`/metadata), retensi, audit.

## 7. Keamanan rantai pasok dan build

- Dependensi dikunci (`go.sum`, version catalog, lockfile npm); pemindaian kerentanan terjadwal (govulncheck, OSV-scanner, `npm audit`, dependabot/renovate).
- Build reproducible sebisanya; biner dan APK ditandatangani; kunci rilis APK di penyimpanan aman dengan akses terbatas.
- Image kontainer minimal (distroless), berjalan non-root, filesystem read-only kecuali volume data.
- Secret (DB password, `SESSION_SECRET`, auth key Tailscale) di env/secret manager, tidak di repo; `run_secret_scanning` di CI.

## 8. Respons insiden (ringkas)

| Kejadian | Tindakan segera |
|---|---|
| HP hilang/dicuri | Revoke device → blokir SIM ke operator → tinjau transaksi 24 jam terakhir → rotasi bila ada kredensial bersama |
| API key bocor | Cabut key → tinjau lonjakan `source=api` → buat key baru → perkuat scope |
| Lonjakan pengiriman tak wajar | Pause semua campaign (tombol darurat "Pause semua"), nonaktifkan SIM terkait, periksa audit |
| Dugaan kompromi server | Isolasi node di tailnet, revoke semua device, rotasi `SESSION_SECRET`/API key/password, pulihkan dari backup bersih |
| Kebocoran data | Identifikasi cakupan via audit, ikuti kewajiban pelaporan hukum yang berlaku |

Tombol darurat "Pause semua pengiriman" (global kill switch, `settings.paused=true`) dibaca dispatcher dan tersedia untuk admin.

## 9. Checklist gerbang rilis

- [ ] Semua endpoint mutasi menolak tanpa CSRF/scope; uji otorisasi negatif per peran lulus.
- [ ] Tidak ada body SMS/secret di log (uji grep pada log integrasi + analisis statis).
- [ ] Token enroll: kedaluwarsa, sekali pakai, brute-force kode manual terbatas — diuji.
- [ ] `device_secret` tidak dapat dipakai pada `/api/v1` selain rute device (uji).
- [ ] Rate limit login/API/WS aktif; uji beban dasar.
- [ ] Header keamanan terpasang; CSP tidak membutuhkan `unsafe-inline`.
- [ ] Ekspor CSV melindungi dari formula injection.
- [ ] Revoke menutup WS ≤ 5 dtk dan menolak reconnect.
- [ ] Backup terenkripsi & uji pulih terdokumentasi.
- [ ] Pemindaian dependensi bersih (tanpa kerentanan tinggi/kritis terbuka).
- [ ] APK: `allowBackup=false`, komponen non-exported kecuali yang perlu, `FLAG_IMMUTABLE` pada PendingIntent, tidak ada cleartext traffic.
