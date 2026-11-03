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
| Cookie sesi | 256-bit, HMAC-SHA256(`SESSION_SECRET`) di DB, rotasi saat login |
| 2FA (TOTP) | RFC 6238 (SHA-1, 6 digit, 30 dtk, toleransi ±1 langkah). Rahasia terenkripsi AES-256-GCM (kunci turunan `WEBHOOK_ENC_KEY`); tiap langkah hanya sekali pakai (atomik di DB, anti-replay); 10 kode pemulihan sekali pakai disimpan ter-hash |
| Enkripsi data rahasia di DB | AES-256-GCM, kunci per tujuan diturunkan dari `WEBHOOK_ENC_KEY`/`SESSION_SECRET`; cincin kunci (kunci lama hanya untuk membaca) + `smscd secrets reencrypt` |

## 4. Kontrol akses

- Otorisasi di server pada setiap handler (matriks spec 08 §3); UI hanya kemudahan.
- Prinsip hak minimal: API key berscope; device hanya `/device/*` dan `/ws/device`; viewer tanpa PII penuh.
- Perubahan peran/pengguna, policy, API key, revoke device, ekspor → audit (aktor, IP/tailnet identity, sebelum/sesudah ringkas).
- Akun admin pertama dibuat via CLI; kebijakan password min 12 karakter dan **kebijakan password baru (fase 7)** — lihat di bawah.
- **2FA TOTP (fase 4)**, opsional per pengguna (disarankan untuk semua admin). Login dua tahap: password benar → hanya *tantangan* tanpa sesi (token tanpa status, 5 menit, terikat sidik jari password sehingga ganti password membatalkannya) → `POST /auth/login/verify` dengan kode. Penghitung gagal **tidak** direset oleh password benar saat 2FA aktif, sehingga menebak kode dibatasi 5 kali lalu akun terkunci 15 menit. Mengaktifkan butuh password; menonaktifkan / menerbitkan ulang kode pemulihan butuh password **dan** kode. Pemakaian kode pemulihan diaudit. Pemulihan darurat: admin lain `DELETE /users/{id}/2fa`, atau `smscd admin reset-2fa --email …` di server (keduanya mencabut semua sesi pengguna tersebut).
- **Kewajiban 2FA (fase 5)**, `PUT /settings/security {require_2fa: off|admin|all}`, **default `off`** (2FA tetap opsional sampai admin menyalakannya). Pengguna dalam cakupan yang belum punya 2FA tetap bisa login tetapi server membalas **403 `auth.mfa_setup_required`** untuk semua endpoint selain akun (`/auth/me|logout|password|2fa|2fa/setup|2fa/enable`) — berlaku juga untuk sesi yang sudah ada, seketika; dashboard hanya menampilkan halaman Akun. API key tidak terpengaruh (bukan pengguna). Pengaman: admin yang belum memakai 2FA tidak boleh menyalakannya (`security.enroll_first`), sehingga tidak ada admin yang mengunci dirinya; bila 2FA sebuah akun direset, akun itu kembali digerbang sampai menyiapkan ulang; pemulihan terakhir via `smscd admin reset-2fa`. Bila pembacaan kebijakan gagal, gerbang **tidak** menutup (fail-open) agar gangguan DB tidak mengunci semua orang.
- **Manajemen pengguna (fase 5)**: `PATCH /users/{id}` (peran / nonaktifkan; berlaku seketika; nonaktif mencabut sesi), `POST /users/{id}/password` (admin menetapkan password; sesi dicabut, kunci akun direset). Mengubah **diri sendiri** ditolak (`user.self_change`), yang sekaligus menjamin selalu ada admin aktif. Semuanya diaudit (perubahan peran dicatat sebelum→sesudah; password tidak pernah dicatat).
- **Kebijakan password baru (fase 7)**, berlaku di semua pintu masuk (admin membuat pengguna, ganti password sendiri, admin menetapkan password, menerima undangan, CLI `admin create|reset-password`): 12–256 karakter (batas atas agar argon2id tidak menjadi vektor DoS); bukan salah satu dari ±47 rb password umum (SecLists/NCSC, MIT, lihat `server/internal/pwpolicy/README.md`); bukan variasi sepele — angka/simbol ≤ 4 karakter di ujung atau huruf diganti angka/simbol (`Password123!`, `p@ssw0rd2024`) yang intinya persis entri daftar; bukan pola monoton (< 5 karakter berbeda), berurutan/berulang, jalur keyboard, atau blok pendek yang diulang; tidak memuat nama akun (bagian email sebelum `@`, ≥ 4 huruf). Penolakan = 422 `terlalu lemah: <alasan>`; pada undangan, penolakan tidak menghabiskan undangan. **Tidak retroaktif**: password lama yang lebih lemah tetap berlaku sampai penggantinya dipilih. Batasan jujur: ini heuristik berbasis daftar, bukan estimator entropi penuh (zxcvbn); frasa panjang yang terdiri dari kata acak diterima, dan password yang bocor tetapi tidak ada di daftar tidak terdeteksi (tidak ada pencarian ke layanan luar, sengaja).
- **Undangan & hapus pengguna (fase 6)**: undangan berupa token 256-bit sekali pakai (hanya hash di DB, tampil sekali, berlaku ≤ 30 hari), penerima memilih passwordnya sendiri sehingga admin tidak pernah memegang kredensial pengguna; penerimaan atomik (aman terhadap balapan), jawaban kegagalan seragam, endpoint publik dibatasi laju. Tautan adalah kredensial sementara — bagikan lewat jalur aman dan cabut bila salah kirim. Hapus pengguna dua langkah (nonaktifkan dahulu), tanpa menghapus jejak audit.
- **Email undangan (fase 8)**: tautan undangan adalah kredensial sementara dan *melewati email*, jadi kerahasiaannya bergantung pada relay SMTP dan kotak surat penerima. Mitigasi di sisi kami: isi email terenkripsi di outbox dan dibuang setelah selesai, TLS wajib secara default dan tanpa downgrade diam-diam, verifikasi sertifikat, kredensial SMTP hanya dari environment (tak pernah ditampilkan API/UI/audit), anti-injeksi header, undangan sekali pakai & berumur pendek yang dicabut otomatis bila diganti. Gunakan relay tepercaya dengan TLS; untuk undangan sensitif tetap bisa dibagikan manual.
- **WebAuthn (fase 9)**: faktor kedua tahan-phishing (kredensial terikat RP ID/origin sehingga tak dapat dipakai di situs tiruan). Kunci privat tidak pernah meninggalkan authenticator; server menyimpan kunci publik + sign count (kolom `data` jsonb, tanpa rahasia). Upacara memakai challenge acak sekali pakai berumur 5 menit yang terikat pengguna & jenis; kegagalan dihitung ke lockout; sign count mundur (kunci diklon) ditolak dan diaudit. Attestation sengaja `none` (tidak membatasi model authenticator, tanpa metadata FIDO). Pemulihan bila kunci hilang: kode pemulihan (diterbitkan saat faktor 2FA pertama), atau reset admin/CLI. **Mengubah RP ID/domain membatalkan semua kunci.**
- **Login tanpa password dengan passkey (fase 10)** — opt-in admin, default mati. Model ancaman: passkey menggabungkan *kepemilikan perangkat* dan *verifikasi pengguna* (PIN/biometrik), sehingga dianggap setara password + faktor kedua; karena itu UV **wajib** (pendaftaran dan login) dan resident key wajib saat mendaftar. Kredensial dibedakan (`passkey` vs kunci keamanan biasa) agar kunci faktor-kedua yang UV-nya hanya "preferred" tidak pernah menggantikan password. Risiko yang diterima/ditandai: passkey platform tersinkron ke akun cloud (iCloud/Google) — keamanan akun gateway ikut bergantung pada akun itu; kehilangan perangkat tanpa PIN kuat = akses. Mitigasi: opt-in global, lencana "tersinkron" terlihat, audit tiap login (`method=passkey`), deteksi klon, revoke lewat hapus kunci / reset admin, kunci akun & penonaktifan akun tetap berlaku.
- **SSO OIDC (fase 11)**. Ancaman yang ditangani: *login CSRF / fiksasi sesi* (state sekali pakai + cookie binder per peramban), *replay* (nonce, code sekali pakai, PKCE S256 menahan pencurian code), *token palsu* (tanda tangan JWKS, `iss`, `aud`, `exp`, algoritma dibatasi), *open redirect* (`next` hanya path lokal), *pengambilalihan akun lewat email IdP* (identitas = `(issuer, sub)`; penautan email hanya opt-in + `email_verified` + belum tertaut), *pembukaan jalur masuk tersembunyi* (penyediaan otomatis mati default, batas domain, peran default tidak pernah admin), *kebocoran detail* (kegagalan hanya berupa kode ringkas; detail di log/audit; rahasia klien tak pernah ke UI/API). Risiko yang diterima/ditandai: kepercayaan penuh pada IdP (kompromi IdP = kompromi gateway; pakai MFA di IdP, `OIDC_TRUST_IDP_MFA=false` bila tidak), tidak ada logout back-channel (nonaktifkan pengguna juga di dashboard), sinkron peran dari grup menimpa perubahan manual.
- **SCIM (fase 12)**. Token SCIM setara kekuatan "mengelola semua pengguna terkelola", jadi dibatasi: token khusus (hash di DB, dapat dicabut, tidak berlaku di REST dan sebaliknya), mati secara default, rate limit, aktor audit; **tidak dapat mencetak/mengubah admin** (default), **tidak menyentuh pengguna lokal** (default), **tidak pernah menyisakan nol admin aktif** (juga untuk perubahan lewat grup: seluruh transaksi dibatalkan); pemetaan grup→peran hanya dari environment (admin butuh `SCIM_ALLOW_ADMIN_ROLE`), grup hanya dapat menaikkan peran; pengguna hasil SCIM tanpa password. Risiko yang diterima: kompromi token/IdP memungkinkan membuat pengguna viewer/operator dan menonaktifkan pengguna terkelola — pantau audit `scim.*`, putar token berkala, aktifkan `SCIM_ALLOW_ADMIN_ROLE` hanya bila perlu.
- **Belum ada**: akun tanpa password via passkey saja, kebijakan "hanya passkey terikat perangkat", logout/back-channel OIDC, banyak IdP. Endpoint akun dibatasi lajunya per pengguna; endpoint login per IP (`LOGIN_RATE_PER_MIN`/`LOGIN_BURST`).

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
| API key bocor | Cabut key (atau **Putar** dengan masa tumpang tindih 0 bila klien harus tetap hidup dengan secret baru) → tinjau lonjakan `source=api` → perkuat scope |
| Lonjakan pengiriman tak wajar | Pause semua campaign (tombol darurat "Pause semua"), nonaktifkan SIM terkait, periksa audit |
| Dugaan kompromi server | Isolasi node di tailnet, revoke semua device, rotasi `SESSION_SECRET` **tanpa** `…_PREVIOUS` (logout massal) dan `WEBHOOK_ENC_KEY` (lihat spec 10 R-06), putar semua API key dan secret webhook dengan grace 0, minta semua pengguna ganti password, pulihkan dari backup bersih |
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
- [ ] APK: `allowBackup=false`, komponen non-exported kecuali yang perlu, PendingIntent hasil SMS memakai intent eksplisit ke komponen non-exported (`FLAG_MUTABLE` diperlukan agar `fillIn` telepon diteruskan; semua PendingIntent lain `FLAG_IMMUTABLE`), tidak ada cleartext traffic.
