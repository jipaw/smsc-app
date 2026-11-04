<!-- DIHASILKAN oleh contracts/tools/gen_api_ref.py dari openapi.yaml. Jangan edit manual. -->
# Daftar endpoint (116 operasi)

Auth: `sessionCookie` = sesi dashboard, `apiKey` = API key sistem eksternal, `deviceToken` = aplikasi Android, `publik` = tanpa auth.

## Auth

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `POST` | `/auth/login` | Login dashboard | publik | 200,401,423,429 |
| `POST` | `/auth/login/verify` | Selesaikan login dengan kode 2FA | publik | 200,401,423,429 |
| `POST` | `/auth/password` | Ganti password sendiri | apiKey/sessionCookie | 204,401,403,422,429 |
| `GET` | `/auth/methods` | Metode login yang tersedia (publik, untuk halaman login) | publik | 200 |
| `POST` | `/auth/passkey/begin` | Mulai login tanpa password dengan passkey | publik | 200,409,429 |
| `POST` | `/auth/passkey/finish` | Selesaikan login tanpa password dengan passkey | publik | 200,400,401,409,423,429 |
| `GET` | `/auth/oidc/login` | Mulai login SSO (navigasi peramban) | publik | 302,404,429 |
| `GET` | `/auth/oidc/callback` | Callback IdP (redirect_uri) | publik | 302,429 |
| `POST` | `/auth/oidc/link` | Mulai menautkan akun saya ke SSO | apiKey/sessionCookie | 200,404,502,403 |
| `DELETE` | `/auth/oidc/link` | Lepas tautan SSO akun saya | apiKey/sessionCookie | 204,409,403 |
| `GET` | `/auth/webauthn` | Kunci keamanan / passkey milik saya | apiKey/sessionCookie | 200,403 |
| `POST` | `/auth/webauthn/register/begin` | Mulai mendaftarkan kunci keamanan (butuh password) | apiKey/sessionCookie | 200,401,409,422,429 |
| `POST` | `/auth/webauthn/register/finish` | Selesaikan pendaftaran kunci keamanan | apiKey/sessionCookie | 201,400,409,422,429 |
| `DELETE` | `/auth/webauthn/credentials/{credentialId}` | Hapus satu kunci keamanan (butuh password + kode) | apiKey/sessionCookie | 204,401,404,429 |
| `POST` | `/auth/login/webauthn/begin` | Mulai login dengan kunci keamanan (setelah password benar) | publik | 200,401,409,423,429 |
| `POST` | `/auth/login/webauthn/finish` | Selesaikan login dengan kunci keamanan | publik | 200,400,401,423,429 |
| `GET` | `/auth/2fa` | Status 2FA akun saya | apiKey/sessionCookie | 200,403 |
| `POST` | `/auth/2fa/setup` | Mulai penyiapan 2FA (TOTP) | apiKey/sessionCookie | 200,401,409,429 |
| `POST` | `/auth/2fa/enable` | Konfirmasi kode pertama dan aktifkan 2FA | apiKey/sessionCookie | 200,409,422,429 |
| `POST` | `/auth/2fa/disable` | Nonaktifkan 2FA (butuh password + kode) | apiKey/sessionCookie | 204,401,409,429 |
| `POST` | `/auth/2fa/recovery-codes` | Terbitkan ulang kode pemulihan (kode lama hangus) | apiKey/sessionCookie | 200,401,409,429 |
| `POST` | `/auth/logout` | Logout | apiKey/sessionCookie | 204 |
| `GET` | `/auth/me` | Profil + peran pengguna saat ini | apiKey/sessionCookie | 200 |
| `POST` | `/invitations/lookup` | Periksa token undangan (halaman undangan, publik) | publik | 200,404,429 |
| `POST` | `/invitations/accept` | Terima undangan dan buat akun (publik) | publik | 201,404,409,422,429 |

## Devices

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `POST` | `/device-enrollments` | Buat token enrollment sekali pakai (untuk QR) | apiKey/sessionCookie | 201,403 |
| `DELETE` | `/device-enrollments/{enrollmentId}` | Cabut token enrollment yang belum dipakai | apiKey/sessionCookie | 204,404 |
| `GET` | `/devices` | Daftar device | apiKey/sessionCookie | 200 |
| `GET` | `/devices/{deviceId}` | Detail device (termasuk SIM) | apiKey/sessionCookie | 200,404 |
| `PATCH` | `/devices/{deviceId}` | Ubah nama, enable/disable, policy | apiKey/sessionCookie | 200 |
| `DELETE` | `/devices/{deviceId}` | Cabut device (revoke kredensial, putus WebSocket kode 4401) | apiKey/sessionCookie | 204 |
| `POST` | `/devices/{deviceId}/commands` | Kirim perintah ke device via WebSocket | apiKey/sessionCookie | 202,409 |

## DeviceAgent

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `POST` | `/device/enroll` | Aplikasi menukar token QR menjadi kredensial device | publik | 201,401 |
| `GET` | `/device/config` | Ambil konfigurasi terbaru (fallback jika WebSocket belum tersambung) | deviceToken | 200,401 |
| `POST` | `/device/events` | Kirim batch event status SMS via REST (fallback outbox flush) | deviceToken | 200 |

## SIMs

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/sims` | Semua SIM lintas device | apiKey/sessionCookie | 200 |
| `GET` | `/sims/{simId}` | Detail SIM + counter hari ini | apiKey/sessionCookie | 200 |
| `PATCH` | `/sims/{simId}` | Label, enable/disable, policy khusus SIM | apiKey/sessionCookie | 200 |

## Messages

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/messages` | Daftar transaksi | apiKey/sessionCookie | 200 |
| `POST` | `/messages` | Kirim SMS non-campaign (1..1000 penerima) | apiKey/sessionCookie | 202,422,429 |
| `GET` | `/messages/export` | Ekspor CSV dengan filter yang sama seperti daftar | apiKey/sessionCookie | 200 |
| `GET` | `/messages/{messageId}` | Detail transaksi + timeline event | apiKey/sessionCookie | 200,404 |
| `POST` | `/messages/{messageId}/cancel` | Batalkan pesan berstatus `queued` atau `assigned` | apiKey/sessionCookie | 200,409 |
| `POST` | `/messages/{messageId}/retry` | Antrekan ulang pesan `failed`/`expired`/`unknown` | apiKey/sessionCookie | 200,409 |
| `GET` | `/saved-views` | Tampilan (filter) transaksi tersimpan milik pengguna yang login | apiKey/sessionCookie | 200,403 |
| `POST` | `/saved-views` | Simpan tampilan baru (maksimal 20 per pengguna; nama unik tanpa membedakan huruf besar/kecil) | apiKey/sessionCookie | 201,409,422 |
| `PUT` | `/saved-views/{viewId}` | Ganti nama dan/atau filter tampilan milik sendiri (milik pengguna lain = 404) | apiKey/sessionCookie | 200,404,409,422 |
| `DELETE` | `/saved-views/{viewId}` | Hapus tampilan milik sendiri | apiKey/sessionCookie | 204,404 |

## Campaigns

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/campaigns` | Daftar campaign (dengan counter) | apiKey/sessionCookie | 200 |
| `POST` | `/campaigns` | Buat campaign (status `draft`) | apiKey/sessionCookie | 201,422 |
| `GET` | `/campaigns/{campaignId}` | Detail + counter + perkiraan selesai | apiKey/sessionCookie | 200,404 |
| `PATCH` | `/campaigns/{campaignId}` | Ubah campaign. Penuh saat `draft`; saat `scheduled`/`running`/`paused` hanya `policy_id` dan `sim_ids`. | apiKey/sessionCookie | 200,404,409,422 |
| `DELETE` | `/campaigns/{campaignId}` | Hapus campaign berstatus `draft` (beserta penerimanya) | apiKey/sessionCookie | 204,404,409 |
| `POST` | `/campaigns/{campaignId}/recipients` | Impor penerima (hanya saat `draft`; boleh berulang, maks. 50.000 per campaign) | apiKey/sessionCookie | 200,404,409,422 |
| `DELETE` | `/campaigns/{campaignId}/recipients` | Hapus semua penerima (hanya saat `draft`), mis. untuk impor ulang setelah memperbaiki CSV | apiKey/sessionCookie | 200,404,409 |
| `POST` | `/campaigns/{campaignId}/start` | Mulai (atau jadwalkan jika `schedule.start_at` di masa depan) | apiKey/sessionCookie | 200,404,409,422 |
| `POST` | `/campaigns/{campaignId}/pause` | Jeda. Pesan `queued` tertahan, yang sedang `sending` diselesaikan. (Pesan tetap tunduk pada TTL.) | apiKey/sessionCookie | 200,404,409 |
| `POST` | `/campaigns/{campaignId}/resume` | Lanjutkan dari `paused`. Evaluasi auto-pause dimulai ulang dari saat ini. | apiKey/sessionCookie | 200,404,409 |
| `POST` | `/campaigns/{campaignId}/cancel` | Batalkan. Semua pesan `queued` dan penerima yang belum dimaterialisasi menjadi `cancelled`; yang sudah di tangan HP diselesaikan. | apiKey/sessionCookie | 200,404,409 |
| `POST` | `/campaigns/{campaignId}/preview` | Render template untuk N penerima contoh + hitung segmen dan estimasi durasi | apiKey/sessionCookie | 200,404,422 |

## Templates

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/templates` | Daftar template | apiKey/sessionCookie | 200 |
| `POST` | `/templates` | Buat template | apiKey/sessionCookie | 201,422 |
| `GET` | `/templates/{templateId}` | Detail | apiKey/sessionCookie | 200 |
| `PUT` | `/templates/{templateId}` | Ganti isi template | apiKey/sessionCookie | 200,422,404 |
| `DELETE` | `/templates/{templateId}` | Hapus | apiKey/sessionCookie | 204 |

## Policies

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/dispatch-policies` | Daftar policy pengiriman | apiKey/sessionCookie | 200 |
| `POST` | `/dispatch-policies` | Buat policy | apiKey/sessionCookie | 201,422 |
| `GET` | `/dispatch-policies/{policyId}` | Detail | apiKey/sessionCookie | 200 |
| `PUT` | `/dispatch-policies/{policyId}` | Ubah. Perubahan di-push ke device (`config.update`) dan berlaku untuk pesan yang belum di-lease. | apiKey/sessionCookie | 200,422,404 |
| `DELETE` | `/dispatch-policies/{policyId}` | Hapus (ditolak jika masih dipakai atau default) | apiKey/sessionCookie | 204,409 |
| `GET` | `/dispatch-policies/{policyId}/effective` | Simulasi policy efektif untuk kombinasi tertentu (debug) | apiKey/sessionCookie | 200 |
| `PUT` | `/settings/default-policy` | Tetapkan policy global default | apiKey/sessionCookie | 204,404 |
| `GET` | `/settings/dispatch-pause` | Status kill switch global (pause semua pengiriman) | apiKey/sessionCookie | 200 |
| `PUT` | `/settings/dispatch-pause` | Aktifkan/nonaktifkan kill switch. Saat aktif dispatcher tidak melepas pesan baru; yang sudah sending diselesaikan. | apiKey/sessionCookie | 200 |

## Compliance

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/suppressions` | Daftar nomor yang diblokir (opt-out / blacklist) | apiKey/sessionCookie | 200 |
| `POST` | `/suppressions` | Tambah nomor (bulk) | apiKey/sessionCookie | 201 |
| `DELETE` | `/suppressions/{suppressionId}` | Hapus dari daftar blokir | apiKey/sessionCookie | 204 |

## Admin

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/api-keys` | Daftar API key (tanpa secret) | apiKey/sessionCookie | 200 |
| `POST` | `/api-keys` | Buat API key. Secret hanya tampil sekali. | apiKey/sessionCookie | 201 |
| `POST` | `/api-keys/{apiKeyId}/rotate` | Putar secret API key | apiKey/sessionCookie | 200,404,422 |
| `DELETE` | `/api-keys/{apiKeyId}` | Cabut | apiKey/sessionCookie | 204 |
| `GET` | `/users` | Daftar pengguna dashboard (admin) | apiKey/sessionCookie | 200 |
| `POST` | `/users` | Buat pengguna | apiKey/sessionCookie | 201,409,422 |
| `DELETE` | `/users/{userId}` | Hapus pengguna (harus sudah dinonaktifkan) | apiKey/sessionCookie | 204,404,409 |
| `PATCH` | `/users/{userId}` | Ubah peran / nonaktifkan pengguna lain | apiKey/sessionCookie | 200,404,409,422 |
| `POST` | `/users/{userId}/password` | Admin menetapkan password baru untuk pengguna lain | apiKey/sessionCookie | 204,404,409,422 |
| `DELETE` | `/users/{userId}/2fa` | Reset 2FA pengguna (kehilangan authenticator) | apiKey/sessionCookie | 204,404 |
| `GET` | `/invitations` | Daftar undangan (200 terbaru; tanpa token) | apiKey/sessionCookie | 200 |
| `POST` | `/invitations` | Buat undangan pengguna (tautan sekali pakai) | apiKey/sessionCookie | 201,409,422 |
| `DELETE` | `/invitations/{invitationId}` | Cabut undangan | apiKey/sessionCookie | 204,404,409 |
| `GET` | `/scim-tokens` | Daftar token SCIM aktif (tanpa nilai token) | apiKey/sessionCookie | 200 |
| `POST` | `/scim-tokens` | Buat token SCIM untuk IdP | apiKey/sessionCookie | 201,409,422 |
| `GET` | `/scim-groups` | Grup SCIM yang diterima dari IdP (ringkasan, 500 teratas) | apiKey/sessionCookie | 200 |
| `DELETE` | `/scim-tokens/{tokenId}` | Cabut token SCIM | apiKey/sessionCookie | 204,404 |
| `GET` | `/settings/scim` | Konfigurasi SCIM yang aktif | apiKey/sessionCookie | 200 |
| `GET` | `/settings/sso` | Konfigurasi SSO OIDC yang aktif (tanpa rahasia) | apiKey/sessionCookie | 200 |
| `GET` | `/settings/email` | Pengaturan SMTP yang sedang aktif (tanpa kredensial) | apiKey/sessionCookie | 200 |
| `POST` | `/settings/email/test` | Kirim email uji (langsung, tanpa outbox) | apiKey/sessionCookie | 200,409,422,429,502 |
| `GET` | `/settings/security` | Kebijakan wajib-2FA | apiKey/sessionCookie | 200 |
| `PUT` | `/settings/security` | Ubah kebijakan wajib-2FA (default `off`) | apiKey/sessionCookie | 200,409,422 |
| `GET` | `/audit-logs` | Jejak audit tindakan admin | apiKey/sessionCookie | 200 |
| `GET` | `/settings/retention` | Pengaturan retensi data + ringkasan eksekusi terakhir | apiKey/sessionCookie | 200 |
| `PUT` | `/settings/retention` | Ubah retensi | apiKey/sessionCookie | 200,422 |

## Webhooks

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/webhooks` | Daftar webhook (tanpa secret) | apiKey/sessionCookie | 200 |
| `POST` | `/webhooks` | Daftarkan webhook. `signing_secret` hanya tampil SEKALI pada respons ini. | apiKey/sessionCookie | 201,422 |
| `GET` | `/webhooks/{webhookId}` | Detail webhook | apiKey/sessionCookie | 200,404 |
| `PATCH` | `/webhooks/{webhookId}` | Ubah url, events, enabled, atau description. Webhook nonaktif tidak menerima event baru; pengiriman tertunda menunggu sampai diaktifkan. | apiKey/sessionCookie | 200,404,422 |
| `DELETE` | `/webhooks/{webhookId}` | Hapus webhook beserta riwayat pengirimannya | apiKey/sessionCookie | 204,404 |
| `POST` | `/webhooks/{webhookId}/rotate-secret` | Putar secret penandatangan | apiKey/sessionCookie | 200,404,422 |
| `POST` | `/webhooks/{webhookId}/test` | Kirim event uji `webhook.test` (tanpa memedulikan langganan event) | apiKey/sessionCookie | 202,404 |
| `GET` | `/webhooks/{webhookId}/deliveries` | Riwayat pengiriman (terbaru dulu) | apiKey/sessionCookie | 200,404 |
| `POST` | `/webhooks/{webhookId}/deliveries/{deliveryId}/retry` | Kirim ulang pengiriman berstatus `failed` (percobaan dimulai dari nol) | apiKey/sessionCookie | 204,404,409 |

## Stats

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/stats/overview` | Ringkasan dashboard | apiKey/sessionCookie | 200 |
| `GET` | `/stats/timeseries` | Deret waktu jumlah per status | apiKey/sessionCookie | 200 |
| `GET` | `/healthz` | Liveness. `/readyz` memeriksa koneksi DB. | publik | 200 |

## Realtime

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/events/stream` | Server-Sent Events untuk dashboard | apiKey/sessionCookie | 200 |
