# Server SMSC (`smscd`)

Go 1.24 + PostgreSQL 16. Satu biner: REST API, WebSocket device, SSE, dispatcher, reaper, dan dashboard (embed).
Spesifikasi: `docs/spec/04`, kontrak: `contracts/`.

## Status (fase 1–13)

| Sudah | Belum (fase 4) |
|---|---|
| Auth admin (argon2id, sesi, CSRF, lockout), RBAC admin/operator/viewer, API key bertingkat scope | back-channel logout, grup SCIM bersarang |
| Enrollment sekali pakai, registry device/SIM, revoke, perintah jarak jauh | Multi-instance: leader tunggal via advisory lock; sejak fase 20 sesi WS dapat tersebar di banyak instance (lihat fase 20) |
| WS hub protokol v1 (hello + rekonsiliasi, heartbeat, sms.event idempoten, sim.report) | |
| Kirim pesan (normalisasi E.164, GSM-7/UCS-2, template, suppression, Idempotency-Key, `client_ref`) | |
| Dispatcher per-SIM: delay acak [min,max], burst, rate menit/jam/hari, jendela waktu, concurrency, kill switch | |
| Reaper: lease/ack timeout, `unknown` (sent timeout), kedaluwarsa, device offline | |
| Mesin status satu pintu, retry + backoff, cancel/retry manual, transaksi + timeline, ekspor CSV aman | |
| Policy berlapis (global/device/SIM/pesan) + CRUD + tampilan efektif, SSE dengan replay, statistik, audit log | |
| **Campaign (fase 2)**: draft, impor penerima JSON/CSV, preview + estimasi, start/jadwal/jeda/lanjut/batal, materializer dengan backpressure, auto-pause, `no_capacity`, deteksi selesai | |
| **Fase 13**: SCIM `Group` → peran (`SCIM_ADMIN_GROUPS`/`SCIM_OPERATOR_GROUPS`/`SCIM_VIEWER_GROUPS`), peran efektif = tertinggi(roles, grup), rekonsiliasi saat start | |
| **Fase 14–15**: rekonsiliasi grup SCIM berkala (`SCIM_RECONCILE_INTERVAL`); status run terakhir tersimpan di database (`settings.scim_reconcile_last`) dan tampil di Pengaturan → SCIM | |
| **Fase 21**: pemulihan pesan `assigned` saat leader berganti (lease diperbarui + kirim ulang, `settings.leader_seen`, `smsc_lease_recovered_total`) | |
| **Fase 20**: WS Hub multi-instance (`device_sessions`, `hub_commands`, `cmd_refs`; relay perintah/Request/close, event SSE, dan wake lewat LISTEN/NOTIFY + poll cadangan; `HUB_TICK`) | |
| **Fase 19**: pemantauan lock leader + pemilihan ulang/ambil-alih standby (`LEADER_TICK`), metrik `smsc_leader_changes_total`, 4 alert leader | |
| **Fase 18**: anotasi restart/deploy/pergantian leader di Grafana, metrik `smsc_build_info` dan `smsc_leader`, alert `SmscRestartLoop` | |
| **Fase 17**: dashboard Grafana SCIM + rekonsiliasi (`ops/grafana/smsc-scim.json`) | |
| **Fase 16**: metrik Prometheus rekonsiliasi SCIM + 2 alert | |
| **Fase 12**: SCIM 2.0 `User` di `/scim/v2` (token khusus, filter, PATCH Azure/Okta, pengaman admin) | |
| **Fase 11**: SSO OIDC (code + PKCE): `/auth/oidc/login|callback|link`, `/settings/sso`; penyediaan otomatis & pemetaan grup opsional | |
| **Fase 10**: login tanpa password dengan passkey (opt-in admin): `/auth/passkey/begin|finish`, `/auth/methods`, `passkey:true` pada pendaftaran | |
| **Fase 9**: WebAuthn (kunci keamanan / passkey) sebagai faktor kedua: `/auth/webauthn*`, `/auth/login/webauthn/*` | |
| **Fase 8**: email undangan lewat SMTP (outbox terenkripsi, retry, STARTTLS/TLS), `GET /settings/email`, `POST /settings/email/test` | |
| **Fase 7**: kebijakan password (daftar umum + pola + kemiripan email) di semua pintu masuk; retensi undangan | |
| **Fase 6**: undangan pengguna (`/invitations`, tautan sekali pakai) dan `DELETE /users/{id}` (harus dinonaktifkan dahulu) | |
| **Fase 5**: kewajiban 2FA (`off`/`admin`/`all`, default `off`) dengan gerbang 403 `auth.mfa_setup_required`; manajemen pengguna (`PATCH /users/{id}`, `POST /users/{id}/password`) | |
| **Fase 4**: 2FA TOTP (+ kode pemulihan, login dua tahap), ganti password, rotasi kredensial device / API key / secret webhook / `SESSION_SECRET` / kunci enkripsi | |
| **Webhook (fase 3)**: outbox transaksional, HMAC-SHA256, retry/backoff, SSRF guard, riwayat + kirim ulang | |
| **Retensi (fase 3)**: pembersihan terjadwal, `PUT /settings/retention`, `smscd privacy erase` | |
| **Metrik (fase 3)**: `/metrics` di listener terpisah, `ops/prometheus/alerts.yml` | |

## Webhook, retensi, metrik (fase 3)

| Env | Default | Arti |
|---|---|---|
| `WEBHOOK_TIMEOUT` | `10s` | Batas waktu per percobaan |
| `WEBHOOK_BACKOFF` | `30s,2m,10m,1h,6h` | Jeda sebelum percobaan ke-2, 3, … (jumlah percobaan = jumlah jeda + 1) |
| `WEBHOOK_TICK` | `1s` | Interval polling outbox (8 worker, lease 2 mnt) |
| `WEBHOOK_ENC_KEY` | turunan `SESSION_SECRET` | Kunci enkripsi secret webhook |
| `WEBHOOK_ALLOW_LOOPBACK` | `false` | Hanya uji/dev |
| `RETENTION_INTERVAL` | `1h` | Frekuensi pembersihan |
| `METRICS_ADDR` | `127.0.0.1:9090` | Listener `/metrics` terpisah; kosong = nonaktif |

Event: `message.sent`, `message.delivered`, `message.failed`, `campaign.completed`, `campaign.paused`, `device.offline` (+ `webhook.test` dari tombol Uji). Header: `X-SMSC-Event`, `X-SMSC-Delivery`, `X-SMSC-Timestamp`, `X-SMSC-Signature: sha256=hex(HMAC(secret, timestamp + "." + body))`.

```bash
smscd privacy erase --number 08123456789 --suppress   # anonimkan semua jejak satu nomor (+ daftar blokir)
curl -s 127.0.0.1:9090/metrics | grep ^smsc_
promtool check rules ../ops/prometheus/alerts.yml     # bila promtool tersedia
```

## Rotasi kredensial dan 2FA (fase 4)

| Env | Default | Arti |
|---|---|---|
| `SESSION_SECRET_PREVIOUS` | – | Rahasia sesi lama (koma); sesi tetap sah selama rotasi |
| `WEBHOOK_ENC_KEY_PREVIOUS` | – | Kunci enkripsi lama (secret webhook & rahasia TOTP) untuk membaca selama rotasi |
| `DEVICE_ROTATE_GRACE` | `60s` | Masa secret device lama setelah rotasi |
| `LOGIN_RATE_PER_MIN` / `LOGIN_BURST` | `5` / `5` | Batas login & verifikasi 2FA per IP (akun: 2× per pengguna) |

```bash
smscd admin reset-2fa --email admin@contoh.id         # admin terkunci keluar (cabut 2FA + semua sesinya)
smscd admin reset-password --email admin@contoh.id    # lupa password (acak, tampil sekali)
smscd secrets reencrypt                               # setelah mengganti WEBHOOK_ENC_KEY (kunci lama di …_PREVIOUS)
```

Endpoint: `POST /auth/login` → (2FA aktif) `{mfa_required, mfa_token}` → `POST /auth/login/verify`; `/auth/2fa`, `/auth/2fa/{setup,enable,disable,recovery-codes}`, `/auth/password`, `/users/{id}/2fa` (admin), `/api-keys/{id}/rotate`, `/webhooks/{id}/rotate-secret`, perintah device `rotate_credential`. Runbook: `docs/spec/10` R-06.

## SCIM 2.0 (fase 12)

| Env | Default | Arti |
|---|---|---|
| `SCIM_ENABLED` | `false` | Aktifkan `/scim/v2` dan pembuatan token |
| `SCIM_DEFAULT_ROLE` | `viewer` | Peran bila `roles` tidak diberikan (`viewer`/`operator`) |
| `SCIM_ALLOW_ADMIN_ROLE` | `false` | Izinkan SCIM memberi/mengubah peran admin |
| `SCIM_ADOPT_EXISTING` | `false` | Pengguna lokal yang sudah ada terlihat dan dapat dikelola SCIM |
| `SCIM_ADMIN_GROUPS` | – | Nama grup IdP → admin (butuh `SCIM_ALLOW_ADMIN_ROLE=true`, jika tidak gagal start) |
| `HUB_TICK` | `1s` | Poll cadangan antar-instance WS Hub (drain `hub_commands`, segarkan sesi instance lain) |
| `LEADER_TICK` | `5s` | Interval pemeriksaan lock leader / percobaan ambil alih oleh standby |
| `SCIM_RECONCILE_INTERVAL` | `1h` | Rekonsiliasi berkala pemetaan grup → peran (leader); `0` = hanya saat start |
| `SCIM_OPERATOR_GROUPS`, `SCIM_VIEWER_GROUPS` | – | Nama grup (dipisah koma, tak peka huruf) → operator / viewer; satu nama tidak boleh di dua peran |

Token: Pengaturan > SCIM atau `POST /api/v1/scim-tokens`. Base URL untuk IdP: `PUBLIC_BASE_URL/scim/v2`. Profil + pemetaan: `contracts/scim.md`.

## SSO OIDC (fase 11)

| Env | Default | Arti |
|---|---|---|
| `OIDC_ISSUER` | – | URL issuer (https; http hanya localhost / `OIDC_ALLOW_INSECURE=true`). Kosong = SSO mati |
| `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` | – | Klien terdaftar di IdP (secret boleh kosong = klien publik + PKCE) |
| `OIDC_REDIRECT_URL` | `PUBLIC_BASE_URL/api/v1/auth/oidc/callback` | Daftarkan persis ini di IdP |
| `OIDC_SCOPES` / `OIDC_NAME` | `openid email profile` / `SSO` | Scope; label tombol |
| `OIDC_AUTO_PROVISION` | `false` | Buat pengguna pada login SSO pertama |
| `OIDC_ALLOWED_DOMAINS` | semua | Batasi domain email untuk penyediaan/penautan otomatis |
| `OIDC_DEFAULT_ROLE` | `viewer` | `viewer`/`operator` |
| `OIDC_GROUPS_CLAIM`, `OIDC_ADMIN_GROUPS`, `OIDC_OPERATOR_GROUPS` | `groups`, – | Pemetaan grup → peran |
| `OIDC_SYNC_ROLES` | `false` | Sinkronkan peran dari grup tiap login |
| `OIDC_LINK_BY_EMAIL` | `false` | Tautkan otomatis ke pengguna lokal bersemail sama (`email_verified` wajib) |
| `OIDC_TRUST_IDP_MFA` | `true` | Sesi SSO memenuhi kewajiban 2FA |
| `OIDC_CA_FILE` | – | CA privat untuk IdP internal |

Alur: `GET /auth/oidc/login?next=` → IdP → `GET /auth/oidc/callback` (302; `?sso_error=` bila gagal). Menautkan: `POST /auth/oidc/link` (sesi) → navigasi ke `url`. Runbook: `docs/spec/10`.

## Passkey (fase 10)

Default mati: `PUT /settings/security {"passkey_login": true}` (admin). Pendaftaran: `POST /auth/webauthn/register/begin {password, name, passkey:true}` (resident key + UV wajib). Login: `POST /auth/passkey/begin` → `navigator.credentials.get()` → `POST /auth/passkey/finish {ceremony_id, credential}` (butuh `userHandle`). Tanpa env baru (memakai `WEBAUTHN_*`).

## WebAuthn (fase 9)

| Env | Default | Arti |
|---|---|---|
| `WEBAUTHN_RP_ID` | host `PUBLIC_BASE_URL` | RP ID (nama domain; **bukan IP**). Mengubahnya membuat semua kunci terdaftar tak berlaku |
| `WEBAUTHN_ORIGINS` | `PUBLIC_BASE_URL` | Origin yang diizinkan (koma) |
| `WEBAUTHN_RP_NAME` | `SMSC Gateway` | Nama yang tampil di dialog browser |

Alur: `POST /auth/webauthn/register/begin|finish` (sesi, butuh password), `GET /auth/webauthn`, `DELETE /auth/webauthn/credentials/{id}` (password + kode); login: `POST /auth/login` → `methods` → `POST /auth/login/webauthn/begin|finish`. Mendaftarkan kunci pertama menerbitkan kode pemulihan.

## Email lewat SMTP (fase 8)

| Env | Default | Arti |
|---|---|---|
| `SMTP_HOST` | – | Kosong = email nonaktif |
| `SMTP_PORT` | 587 / 465 / 25 menurut mode | |
| `SMTP_MODE` | `starttls` | `starttls` (wajib STARTTLS), `tls` (implisit), `none` (tanpa enkripsi; relay lokal tepercaya saja) |
| `SMTP_FROM` | – (wajib bila host diisi) | mis. `SMSC Gateway <smsc@contoh.id>` |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | – | AUTH PLAIN (hanya lewat TLS atau ke loopback); keduanya atau tidak sama sekali |
| `SMTP_CA_FILE` | – | CA privat (PEM) untuk relay internal |
| `SMTP_TIMEOUT` | `15s` | Batas waktu per pengiriman |
| `EMAIL_BACKOFF` / `EMAIL_TICK` | `1m,5m,30m,2h` / `2s` | Jeda retry / interval outbox |

`POST /invitations {..., send_email: true}`; status per undangan di `GET /invitations` (`email_status`: queued|sent|failed|cancelled). Runbook: `docs/spec/10` R-06b.

## Kebijakan password dan retensi undangan (fase 7)

`internal/pwpolicy` (daftar umum di-embed, `go:embed`) dipanggil oleh service untuk: `POST /users`, `POST /auth/password`, `POST /users/{id}/password`, `POST /invitations/accept`, serta CLI `admin create|reset-password` (password acak CLI dicek dan diulang bila perlu). Tanpa env/konfigurasi. Retensi: `PUT /settings/retention` menerima `invitation_retention_days` (opsional, default 90).

## Undangan dan hapus pengguna (fase 6)

`POST /invitations {email, role, ttl_hours?}` → `{token, invite_url}` (tampil sekali; `invite_url = PUBLIC_BASE_URL + /invite/<token>`). Publik (dibatasi laju): `POST /invitations/lookup {token}`, `POST /invitations/accept {token, password}`. Admin: `GET /invitations`, `DELETE /invitations/{id}`, `DELETE /users/{id}` (setelah `PATCH disabled`). Tidak ada pengiriman email.

## Kewajiban 2FA dan manajemen pengguna (fase 5)

`GET/PUT /settings/security` → `{require_2fa: off|admin|all}` (default `off`; tidak ada env). Akun dalam cakupan tanpa 2FA hanya mencapai `/auth/me|logout|password|2fa|2fa/setup|2fa/enable`; sisanya 403 `auth.mfa_setup_required`. `PATCH /users/{id}` `{role?, disabled?}` dan `POST /users/{id}/password` `{new_password}` — keduanya menolak akun sendiri (409 `user.self_change`). Runbook: `docs/spec/10` R-06b.

## Campaign — perilaku yang perlu diketahui

- **Impor** (`POST /campaigns/{id}/recipients`): JSON atau CSV (BOM, `,`/`;`, kolom `phone`, `client_ref`, sisanya variabel; sel kosong = variabel tidak ada). Nomor dinormalisasi ke E.164, **satu nomor sekali per campaign**, daftar blokir dilewati. Variabel per baris hanya bisa diperiksa saat impor jika isi pesan sudah diketahui; selain itu diperiksa pada `preview` dan **wajib lolos sebelum `start`** (422 dengan jumlah penerima per variabel).
- **Materializer**: mengubah penerima menjadi pesan per batch ≤ 1.000, dan **menahan diri bila pesan `queued` campaign ≥ `CAMPAIGN_QUEUE_CAP`** (default 20.000) agar antrean/tabel tidak membengkak. Terukur: 50.000 penerima diimpor 2,6 dtk, materialisasi ±2.000 pesan/dtk.
- **Auto-pause** menilai **percobaan** (event `sent` vs `requeued/failed` berkode infrastruktur: `no_service, radio_off, generic_failure, os_rate_limited, ack_timeout, sim_unavailable, device_lost, device_revoked`) pada `window` terakhir sejak baseline (start/resume), butuh ≥ min(window,10) sampel. `invalid_destination` dan kode non-infrastruktur lain tidak dihitung.
- **`no_capacity`**: ada pekerjaan tersisa tetapi tak ada SIM online selama `NO_CAPACITY_AFTER` (default 10 mnt).
- **Selesai** = semua penerima termaterialisasi dan tidak ada pesan `queued/assigned/sending`. Pesan `sent` (menunggu laporan delivery) dan `unknown` tidak menahan status selesai.
- **Prioritas** campaign default 3 < kirim cepat 5. Policy campaign adalah *preferensi* (tidak bisa melonggarkan guardrail SIM/device). Perubahan `policy_id`/`sim_ids` saat berjalan berlaku untuk pesan yang belum di-lease.
- **TTL**: pesan campaign kedaluwarsa menurut `message_ttl_seconds` sejak dimaterialisasi (juga saat campaign dijeda). Untuk campaign panjang, naikkan TTL di policy campaign.
- Isi template dibaca **saat materialisasi**: mengubah template di tengah campaign memengaruhi batch berikutnya.
- Estimasi durasi kasar (jeda rata-rata + 2 dtk kirim, batas per jendela hanya bila jumlah per SIM melampauinya); mengabaikan jendela waktu dan retry.

## Menjalankan

```bash
export DATABASE_URL='postgres://user:pass@localhost:5432/smsc?sslmode=disable'
export PUBLIC_BASE_URL='https://smsc.<tailnet>.ts.net'     # dipakai untuk enroll_url/ws_url
export SESSION_SECRET="$(openssl rand -hex 32)"
export SECURE_COOKIES=false                                   # hanya untuk dev tanpa HTTPS

go run ./cmd/smscd migrate
go run ./cmd/smscd admin create --email admin@example.com     # password acak dicetak sekali
go run ./cmd/smscd serve                                       # 127.0.0.1:8080
```

Variabel lain: lihat `internal/config/config.go` dan `docs/spec/10` §2. Di produksi biarkan `HTTP_ADDR` di loopback
dan publikasikan lewat Tailscale Serve (docs/04).

> Policy default bawaan memakai jendela kirim **08:00–20:00 Asia/Jakarta**. Di luar jam itu pesan menunggu `queued`
> (perilaku benar). Ubah di dashboard/`PUT /dispatch-policies/{id}`; `00:00`–`00:00` berarti sepanjang hari.

## Tes

```bash
make unit                 # policy, SMS (segmen, nomor, template)
make integration          # Postgres asli + simulator HP lewat WebSocket (butuh DB, lihat DB=...)
make contract             # biner nyata; tiap respons divalidasi terhadap contracts/openapi.yaml
make schema               # contracts/schema.sql == hasil semua migrasi (pg_dump)
make load                 # beban campaign 50.000 penerima (ukur impor, materialisasi, latensi API)
```

Tes integrasi membuat dan menghapus database sementara sendiri; set `SMSC_TEST_DATABASE_URL` ke database admin
(`.../postgres`). Tanpa variabel itu, tes integrasi dilewati. `SMSC_TEST_LOG=1` menampilkan log server saat tes.

## Catatan desain yang perlu diketahui

- `attempt` pada pesan `queued` setelah kegagalan adalah nomor percobaan **berikutnya** (naik saat requeue); event dari attempt lama dicatat sebagai `stale_event`.
- Timer reaper (`sent_timeout`) memakai policy **global**; ack timeout memakai policy efektif (global+device+SIM) saat lease.
- "Hari ini" pada statistik memakai tengah malam zona waktu sesi database (UTC).
- Pemeriksaan jeda/inflight/rate dilakukan di dalam transaksi penugasan di bawah kunci baris SIM (mencegah balapan dengan event `sent`; bug ini sempat tertangkap oleh tes).
