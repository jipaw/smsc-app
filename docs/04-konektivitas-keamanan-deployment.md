# 04 — Konektivitas, Keamanan, dan Deployment

## 1. Topologi jaringan

- Server dan semua HP bergabung ke satu tailnet. Server **tidak** dibuka ke internet publik; tidak ada port-forward.
- Server membind HTTP hanya di `127.0.0.1:8080`; **Tailscale Serve** menerjemahkannya menjadi HTTPS di tailnet:

```bash
tailscale serve --bg --https=443 http://127.0.0.1:8080
```

- Prasyarat tailnet: MagicDNS dan HTTPS certificates diaktifkan di admin console. Nama layanan: `https://smsc.<nama-tailnet>.ts.net`.
- Aplikasi Android hanya menerima skema `https`/`wss` (tanpa izin cleartext), sehingga tidak ada pengecualian `networkSecurityConfig`.

### ACL tailnet (HuJSON, contoh)

```jsonc
{
  "groups": { "group:smsc-admins": ["admin1@example.com"] },
  "tagOwners": {
    "tag:smsc-server": ["autogroup:admin"],
    "tag:smsc-device": ["autogroup:admin"]
  },
  "acls": [
    { "action": "accept", "src": ["tag:smsc-device"],     "dst": ["tag:smsc-server:443"] },
    { "action": "accept", "src": ["group:smsc-admins"],   "dst": ["tag:smsc-server:443"] },
    { "action": "accept", "src": ["tag:smsc-server"],     "dst": ["tag:smsc-server:*"] }
  ]
}
```

Efek: HP hanya bisa menjangkau server pada 443; HP tidak bisa saling menjangkau atau menjangkau perangkat lain di tailnet. Device ber-*tag* tidak terkena key expiry (nonaktifkan expiry untuk node gateway).

## 2. Lapisan autentikasi

| Aktor | Mekanisme | Catatan |
|---|---|---|
| Admin (browser) | Email + password (argon2id) → cookie sesi `HttpOnly; Secure; SameSite=Lax` + token CSRF untuk mutasi. Opsional: percayai header `Tailscale-User-Login` dari Tailscale Serve (hanya jika koneksi berasal dari loopback) untuk SSO tanpa password, dipetakan ke `users`. | Rate limit login, lockout bertahap. |
| Sistem eksternal | API key `smsc_live_<random>` di `Authorization: Bearer`; disimpan ter-hash, punya scope, kedaluwarsa opsional, dapat dicabut. | Hanya dapat dipakai dari tailnet (ACL). |
| Device | `device_secret` 256-bit acak, diterbitkan saat enroll, dikirim sebagai Bearer pada upgrade WS & REST device. Disimpan ter-hash di server, di HP dalam Android Keystore (AEAD). | Dapat dirotasi (`rotate_credential`) dan dicabut. |
| Enrollment | Token 256-bit acak (+ kode manual 8 karakter), 15 menit, sekali pakai, ter-hash; kode manual dibatasi percobaan (5×) | Mencegah pemakaian ulang foto QR. |

Defense in depth: kebocoran satu lapis (mis. device_secret) tidak memberi akses ke REST admin karena skema auth berbeda dan `deviceToken` hanya valid pada rute `/device/*` dan `/ws/device`.

## 3. Ancaman utama dan mitigasi

| Ancaman | Mitigasi |
|---|---|
| HP dicuri/hilang | Admin revoke → WS tertutup `4401`, kredensial mati; SIM tetap risiko fisik → dokumentasikan prosedur blokir SIM ke operator. |
| QR enrollment bocor | TTL pendek, sekali pakai, dapat dicabut (`DELETE /device-enrollments/{id}`). |
| Penyalahgunaan API untuk spam | Scope + rate limit API key, suppression list, kuota harian per SIM, audit log, auto-pause campaign. |
| SMS ganda akibat retry | Dedup `message_id` di device, status `unknown`, tidak ada auto-retry untuk kasus tak pasti. |
| Server dikompromikan → HP dipakai kirim sembarang | Guardrail lokal (min gap, rate, os guard) + enum perintah terbatas; tidak ada eksekusi kode jarak jauh. |
| Kebocoran PII | Masking untuk viewer, retensi body, log tanpa body, backup DB terenkripsi. |
| Replay/duplikasi event | `event_id` unik, `attempt` monoton, `Idempotency-Key`. |
| Brute force login | Rate limit + lockout; akses admin hanya dari tailnet/ACL. |

## 4. Deployment server

Dua bentuk, pilih salah satu:

**A. Docker Compose dengan sidecar Tailscale** (portabel)

```yaml
services:
  tailscale:
    image: tailscale/tailscale:stable
    hostname: smsc
    environment:
      TS_AUTHKEY: ${TS_AUTHKEY}          # auth key bertag tag:smsc-server
      TS_EXTRA_ARGS: --advertise-tags=tag:smsc-server
      TS_SERVE_CONFIG: /config/serve.json
      TS_STATE_DIR: /var/lib/tailscale
    volumes: ["ts-state:/var/lib/tailscale", "./serve.json:/config/serve.json:ro"]
  server:
    image: smsc/server:latest
    network_mode: service:tailscale      # server berbagi netns, listen 127.0.0.1:8080
    environment:
      DATABASE_URL: postgres://smsc:${DB_PASSWORD}@db:5432/smsc
      TRUST_TAILSCALE_HEADERS: "true"
    depends_on: [db]
  db:
    image: postgres:16
    volumes: ["pg:/var/lib/postgresql/data"]
volumes: { ts-state: {}, pg: {} }
```

> Catatan: dengan `network_mode: service:tailscale`, hostname `db` tidak dapat di-resolve dari `server` kecuali `db` juga memakai jaringan yang sama atau `DATABASE_URL` diarahkan ke alamat yang dapat dijangkau; hal ini diselesaikan saat implementasi (mis. `db` ikut `network_mode`, atau Tailscale di host). Struktur final ditetapkan di dokumentasi deployment.

**B. Tailscale di host + biner/systemd**: `tailscale up --advertise-tags=tag:smsc-server`, jalankan server di loopback, `tailscale serve` seperti §1. Paling sederhana untuk VM tunggal.

Backup: `pg_dump` terjadwal (harian) + uji restore; `device_credentials` ter-hash sehingga backup tidak membocorkan secret.

## 5. Panduan setup HP (sumber untuk dokumentasi pengguna)

1. Pasang **Tailscale**, login, pastikan node ber-tag `tag:smsc-device` (admin dapat membuat auth key bertag lebih dulu), aktifkan *Always-on VPN* dan *Block connections without VPN* bila ingin ketat.
2. Pasang APK SMSC (izinkan instalasi dari sumber ini).
3. Buka app → wizard izin. Jika izin SMS abu-abu: *Pengaturan → Aplikasi → SMSC → ⋮ → Izinkan setelan terbatas*, lalu ulangi.
4. Scan QR dari dashboard (*Devices → Tambah device*).
5. Hardening ketahanan (wizard menampilkan checklist, dengan panduan per merek):
   - Pengecualian optimasi baterai untuk **SMSC dan Tailscale**.
   - Izinkan *auto-start* / *run in background* (Xiaomi, Oppo, Vivo, Huawei) dan keluarkan dari "sleeping apps" (Samsung).
   - Kunci app di *recent apps* bila vendor menyediakannya.
   - HP tetap dicolok ke daya; matikan *adaptive battery*; hindari mode hemat daya.
   - Nonaktifkan "Auto-optimize"/"Auto-restart" mingguan vendor.

## 6. Observability dan operasi

- **Metrik Prometheus:** `smsc_devices{status}`, `smsc_ws_connections`, `smsc_queue_depth{priority}`, `smsc_messages_total{status,source}`, `smsc_dispatch_latency_seconds`, `smsc_sim_next_eligible_lag_seconds`, `smsc_event_ingest_total`, `smsc_lease_expired_total`.
- **Log:** JSON terstruktur dengan `device_id`, `message_id`, `trace_id`; tanpa `body`.
- **Alert awal:** semua device offline > 5 menit; antrean > ambang & tidak turun 10 menit; rasio gagal 15 menit > 30%; SIM `no_service` > 15 menit; baterai device < 15% tidak mengisi.
- **Log device:** `device.command upload_logs` mengunggah ring buffer log terbatas (tanpa isi SMS) untuk diagnosa.
- **Versi & update app:** `app_version` dilaporkan; server dapat menolak versi di bawah minimum (`4426`). Distribusi update APK (unduh dari server, install manual/MDM) dijadwalkan v1.1.
