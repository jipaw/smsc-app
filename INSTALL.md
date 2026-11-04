# Instalasi SMSC Gateway

Dari nol sampai SMS pertama. Server (`smscd`) hanya mendengarkan di `127.0.0.1`; dashboard, API, dan HP menjangkaunya lewat **Tailscale** (`tailscale serve`, HTTPS otomatis). Tidak ada port yang dibuka ke internet.

Pilih salah satu:

- **A. Docker Compose** (disarankan): PostgreSQL + smscd + sidecar Tailscale dalam satu berkas.
- **B. Biner + systemd**: Tailscale dan PostgreSQL di host.

## 0. Persiapan Tailscale (sekali)

1. Di admin console Tailscale → *Access controls*, tambahkan tag dan ACL minimal. HP hanya boleh menjangkau server di port 443, dan HP tidak boleh saling menjangkau:

   ```json
   {
     "groups": { "group:smsc-admins": ["anda@contoh.id"] },
     "tagOwners": {
       "tag:smsc-server": ["autogroup:admin"],
       "tag:smsc-device": ["autogroup:admin"]
     },
     "acls": [
       { "action": "accept", "src": ["tag:smsc-device"],   "dst": ["tag:smsc-server:443"] },
       { "action": "accept", "src": ["group:smsc-admins"], "dst": ["tag:smsc-server:443"] }
     ]
   }
   ```

   ACL inilah kontrol jaringan yang sebenarnya. `REQUIRE_VPN` di server hanya membaca laporan HP sendiri, jadi bukan kontrol keamanan.
2. *Settings → Keys*: buat auth key ber-tag `tag:smsc-server` (sekali pakai) untuk server. Untuk HP, buat auth key ber-tag `tag:smsc-device`.
3. Aktifkan HTTPS (*DNS → HTTPS Certificates*). Alamat server nantinya `https://<hostname>.<tailnet>.ts.net`.

## A. Docker Compose

Butuh Docker Engine 24+ dengan plugin compose.

```bash
git clone <repo> smsc && cd smsc
docker build -t smsc/server:latest --build-arg VERSION=$(git describe --always) .
cd deploy/compose
./init-secrets.sh            # membuat secrets/ (password DB, SESSION_SECRET, WEBHOOK_ENC_KEY) — cadangkan!
cp .env.example .env         # isi TS_AUTHKEY, TS_HOSTNAME, PUBLIC_BASE_URL
docker compose up -d
docker compose exec server smscd doctor
docker compose exec server smscd admin create --email anda@contoh.id
```

- `smscd admin create` tanpa `--password` mencetak password acak satu kali.
- Migrasi skema dijalankan otomatis saat start (`AUTO_MIGRATE=true`).
- Di balik proxy perusahaan dengan inspeksi TLS, tambahkan `--secret id=ca_bundle,src=/path/ca.crt` pada `docker build`.
- Rahasia dibaca dari berkas (`*_FILE`), bukan variabel env, sehingga tidak terlihat di `docker inspect`.

## B. Biner + systemd

Butuh Go 1.24+, Node 22 (untuk membangun dashboard), PostgreSQL 16, dan Tailscale di host.

```bash
cd server && make web build && sudo install -m 0755 bin/smscd /usr/local/bin/smscd
sudo useradd --system --home /nonexistent --shell /usr/sbin/nologin smscd
sudo -u postgres createuser smsc --pwprompt && sudo -u postgres createdb -O smsc smsc
sudo install -d -m 0750 -o root -g smscd /etc/smscd
head -c 48 /dev/urandom | base64 | sudo tee /etc/smscd/session_secret >/dev/null
head -c 48 /dev/urandom | base64 | sudo tee /etc/smscd/webhook_enc_key >/dev/null
echo 'postgres://smsc:<password>@127.0.0.1:5432/smsc' | sudo tee /etc/smscd/database_url >/dev/null
sudo chmod 0600 /etc/smscd/session_secret /etc/smscd/webhook_enc_key /etc/smscd/database_url
sudo install -m 0640 -g smscd ../deploy/systemd/smscd.env.example /etc/smscd/smscd.env   # lalu edit
sudo install -m 0644 ../deploy/systemd/smscd.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now smscd
sudo tailscale up --advertise-tags=tag:smsc-server
sudo tailscale serve --bg --https=443 http://127.0.0.1:8080
```

Untuk menjalankan `smscd doctor` dan `smscd admin create` di host, sediakan variabel yang sama:

```bash
sudo env $(sudo cat /etc/smscd/smscd.env | grep -v '^#' | xargs) DATABASE_URL_FILE=/etc/smscd/database_url SESSION_SECRET_FILE=/etc/smscd/session_secret WEBHOOK_ENC_KEY_FILE=/etc/smscd/webhook_enc_key smscd doctor
```

Rahasia di-*mount* oleh systemd (`LoadCredential`), sehingga hanya proses smscd yang dapat membacanya.

## C. Tanpa sidecar: smscd sebagai node tailnet (tsnet)

Varian ini juga memverifikasi identitas node HP di server. Compose: `docker compose -f deploy/compose/docker-compose.tsnet.yml up -d`.

1. Buat auth key `tag:smsc-server` di admin console, lalu simpan di `secrets/tailnet_auth_key`. Key ini hanya untuk login pertama; kunci node disimpan di volume `tailnet`.
2. (Disarankan) Buat **OAuth client** di *Settings → OAuth clients* dengan scope **Auth Keys (write)** dan tag `tag:smsc-device`. Isi `TAILSCALE_OAUTH_CLIENT_ID` di `.env` dan secret-nya di `secrets/tailscale_oauth_secret`. Setiap QR enrollment kemudian memuat auth key device sekali pakai, sehingga HP dengan build tailnet ter-embed tidak butuh aplikasi Tailscale.
3. Mulai dengan `TAILNET_DEVICE_POLICY=record`. Setelah semua HP tampil "tailnet ✓" di Devices, ubah ke `enforce`.

Biner/systemd: set variabel yang sama (`TAILNET_ENABLED=true`, `TAILNET_STATE_DIR=/var/lib/smscd/tailnet`, …) dan lewati langkah `tailscale serve`. Daftar lengkap: `docs/spec/10` §2.

## 1. Periksa dengan `smscd doctor`

`doctor` memeriksa konfigurasi wajib, sumber rahasia, `PUBLIC_BASE_URL` vs `WEBAUTHN_RP_ID`, alamat bind, allow-list tujuan, SMTP/SSO, koneksi dan migrasi database, admin pertama, dan kebijakan 2FA. Kode keluar 1 bila ada `FAIL`. Perbaiki semua `FAIL`; baca setiap `WARN`.

## 2. Masuk dan siapkan

1. Buka `https://<hostname>.<tailnet>.ts.net` dari perangkat anggota `group:smsc-admins`, lalu login dengan admin yang dibuat.
2. Disarankan: aktifkan 2FA di halaman **Akun**. Kewajiban 2FA untuk semua admin (Pengaturan → Keamanan) **nonaktif secara default**.
3. **Pengaturan → API key**: buat key untuk aplikasi Anda (tampil sekali).

## 3. Daftarkan HP

1. Di HP: pasang Tailscale, login dengan auth key `tag:smsc-device`, aktifkan *Always-on VPN*.
2. Pasang APK SMSC, jalankan wizard izin.
3. Dashboard → **Devices → Tambah device** → pindai QR dari aplikasi.
4. Pastikan device **online** dan SIM terdeteksi di dashboard.

## 4. SMS pertama

```bash
curl -sS https://<hostname>.<tailnet>.ts.net/api/v1/messages \
  -H "Authorization: Bearer <API_KEY>" -H "Content-Type: application/json" \
  -d '{"recipients":[{"to":"0812xxxxxxx"}],"body":"Halo dari SMSC"}'
```

Status pesan terlihat di **Transaksi**. Nomor di luar `ALLOWED_DESTINATION_PREFIXES` (default compose: `+62`) ditolak dengan alasan `destination_not_allowed`.

## Operasional

| Topik | Langkah |
|---|---|
| Backup | `pg_dump -Fc` harian dan uji restore berkala. **Cadangkan juga `WEBHOOK_ENC_KEY`** (tanpanya secret webhook dan TOTP di backup tidak dapat dibuka). Kredensial device tersimpan sebagai hash. |
| Upgrade | Compose: bangun image baru, lalu `docker compose up -d` (migrasi otomatis). systemd: ganti biner, lalu `systemctl restart smscd` (`ExecStartPre` menjalankan migrasi). |
| Rotasi `SESSION_SECRET` | Pindahkan nilai lama ke `SESSION_SECRET_PREVIOUS`, isi yang baru, lalu restart. Sesi lama tetap sah selama masa transisi. |
| Rotasi / pemisahan `WEBHOOK_ENC_KEY` | Isi kunci baru, pindahkan yang lama ke `WEBHOOK_ENC_KEY_PREVIOUS`, lalu restart dan jalankan `smscd secrets reencrypt`. Instalasi lama yang belum punya `WEBHOOK_ENC_KEY` sendiri memakai `SESSION_SECRET` sebagai kunci lama. |
| Identitas klien | Di belakang `tailscale serve` semua klien terlihat sebagai `127.0.0.1`. smscd memakai `X-Forwarded-For` dari `TRUSTED_PROXIES` (default loopback) dan, bila `TRUST_TAILSCALE_HEADERS=true`, `Tailscale-User-Login` untuk rate limit dan penguncian login. Proxy di alamat lain wajib ditambahkan ke `TRUSTED_PROXIES`. |
| Akun terkunci | Penguncian berlaku per (akun, sumber): 5 kegagalan → 15 menit, dan 50 kegagalan total per akun → 15 menit. Buka lebih cepat dengan `smscd admin reset-password --email <email>`. |
| Kuota harian | Batasi biaya per API key/pengguna (satuan bagian SMS): default lewat `DAILY_QUOTA_PER_KEY`/`DAILY_QUOTA_PER_USER`, per subjek di Pengaturan → API key / Pengguna. Dipadukan dengan `ALLOWED_DESTINATION_PREFIXES`, kebocoran API key tidak dapat menghabiskan pulsa tanpa batas. |
| Retensi | Isi pesan dan event diatur di **Pengaturan → Retensi**. Audit log: `AUDIT_RETENTION_DAYS` (0 = selamanya). Outbox email yang selesai dihapus setelah 30 hari. |
| Monitoring | `METRICS_ADDR` (default `127.0.0.1:9090/metrics`), alert di `ops/prometheus/alerts.yml`, dashboard Grafana di `ops/grafana/`. Lihat `docs/spec/10-panduan-operasional.md`. |
| Admin kehilangan 2FA | `smscd admin reset-2fa --email <email>` |

Daftar lengkap variabel lingkungan: `docs/spec/10-panduan-operasional.md` §2.
