# Spec 04 — Desain Backend (`smscd`)

Go, satu biner. Kontrak: `contracts/openapi.yaml`, `contracts/device-ws-protocol.md`, skema: `contracts/schema.sql`.

## 1. Struktur kode

```
cmd/smscd/            main: config, wiring, graceful shutdown
internal/
  api/                handler HTTP (hasil oapi-codegen + implementasi), middleware auth/RBAC/CSRF/ratelimit
  ws/                 Hub, sesi device, codec envelope, pembatasan rate
  dispatch/           Dispatcher, lane, policy resolver, routing
  messages/           service pesan: create, transisi status (satu pintu), event
  campaigns/          service campaign, materializer, auto-pause
  devices/            registry, enrollment, SIM sync, status evaluator
  reaper/             lease, sent_timeout, expiry, offline detector
  events/             event bus + SSE broker (ring buffer)
  sms/                normalisasi E.164, encoding & segmen GSM-7/UCS-2, template render
  store/              sqlc (query tertulis), transaksi, migrasi
  auth/               argon2id, sesi, API key, device secret, tailscale identity
  retention/          pembersihan body/event
  obs/                slog, metrics, health
web/                  dashboard (build → embed)
```

Aturan: **hanya paket `messages` yang boleh mengubah `messages.status`**; paket lain memanggil fungsi transisinya. Ini menegakkan invarian state machine (docs/03 §3).

## 2. Konfigurasi proses (env)

Lihat tabel lengkap di spec 10 §2. Inti: `DATABASE_URL`, `HTTP_ADDR` (default `127.0.0.1:8080`), `PUBLIC_BASE_URL`, `TRUST_TAILSCALE_HEADERS`, `SESSION_SECRET`, `DEFAULT_REGION`, `LOG_LEVEL`.

## 3. Transisi status (satu pintu)

```go
// messages.Transition: dipanggil dalam transaksi pemanggil
func (s *Service) Transition(ctx, tx, id uuid.UUID, to Status, ev EventInput) (Message, error)
```

Langkah: `SELECT … FOR UPDATE` baris pesan → validasi `(from,to)` terhadap tabel graf → terapkan perubahan kolom (timestamp, `error_code`, `attempt`, `lease`) → `INSERT message_events` → publish ke event bus **setelah commit**. Transisi yang tidak valid mengembalikan `ErrInvalidTransition` (HTTP 409). Event device duplikat (`device_event_id` unik) dikembalikan sebagai sukses idempoten.

## 4. Dispatcher

### 4.1 Model

Satu goroutine pengendali per proses (`Dispatcher.Run`) memegang `map[simID]*Lane`. Lane adalah struct memori yang **dapat dibangun ulang dari DB**:

```go
type Lane struct {
    SimID, DeviceID uuid.UUID
    NextEligibleAt  time.Time   // juga disimpan di sims.next_eligible_at
    Inflight        int
    BurstCount      int
    Policy          EffectivePolicy   // cache; dibatalkan saat policy/simgrup berubah
}
```

Pemicu `Wake(simID?)`: pesan baru masuk, pesan menjadi terminal/`sent`, device online, policy berubah, campaign resume, timer `NextEligibleAt`/jendela buka. Pemicu digabung (coalesce) agar tidak ada badai query. Tidak ada polling interval tetap kecuali **tick pengaman 5 dtk**.

### 4.2 Siklus satu lane

```text
for each lane yang di-wake:
   if !ready(lane, now): schedule timer ke waktu terdekat (next_eligible, window_open, rate_free) ; continue
   tx:
      msg := pickMessage(lane)                 -- SQL §4.3
      if none: continue
      Transition(msg, assigned, {sim, device, lease=now+ack_timeout, attempt?})
   ws.Send(device, sms.send{...})              -- setelah commit; jika device tak terhubung: rollback ke queued (reaper)
   start timer ack_timeout (pada reaper, bukan per-lane)
```

`attempt` bertambah ketika pesan pernah `sending` sebelumnya (retry setelah gagal); untuk requeue karena `ack_timeout` pada attempt yang belum pernah ack, `attempt` tetap sama dan `sms.send` dikirim ulang dengan `attempt` yang sama (device men-dedup).

### 4.3 SQL pengambilan

```sql
WITH c AS (
  SELECT m.id
  FROM messages m
  LEFT JOIN campaigns c ON c.id = m.campaign_id
  WHERE m.status = 'queued'
    AND m.not_before <= now()
    AND m.expires_at  >  now()
    AND (m.campaign_id IS NULL OR c.status = 'running')
    AND (cardinality(m.route_sim_ids)    = 0 OR $sim_id    = ANY (m.route_sim_ids))
    AND (cardinality(m.route_device_ids) = 0 OR $device_id = ANY (m.route_device_ids))
    AND (m.campaign_id IS NULL OR cardinality(c.sim_ids) = 0 OR $sim_id = ANY (c.sim_ids))
  ORDER BY m.priority DESC, m.not_before, m.id
  FOR UPDATE OF m SKIP LOCKED
  LIMIT 1)
UPDATE messages m
   SET status='assigned', sim_id=$sim_id, device_id=$device_id,
       attempt = GREATEST(m.attempt,1), assigned_at=now(), lease_expires_at=now()+$ack_timeout
  FROM c WHERE m.id = c.id
RETURNING m.*;
```

Catatan: pada pesan yang sedang mengantre untuk SIM yang tidak termasuk allow-list, `ORDER BY … LIMIT 1` dapat "tertahan" di depan pesan lain (head-of-line). Mitigasi: kondisi allow-list berada di `WHERE` (di atas) sehingga hanya pesan yang cocok yang terpilih; indeks parsial `messages_dispatch_idx` menutup urutan, filter tambahan murah pada skala target. Bila terukur lambat (> 50 ms), tambahkan `route_key` terdenormalisasi.

### 4.4 Syarat `ready(lane)`

```text
sim.enabled && sim.status=active && device.enabled && device.status in (online, degraded dengan alasan yang mengizinkan)
&& now >= lane.NextEligibleAt && lane.Inflight < policy.MaxInflight
&& inWindow(policy.SendWindow, now) && !rateLimited(lane) && !osGuardReached(device)
```

`degraded` yang **tidak** mengizinkan kirim: `permission_denied`, semua SIM tidak terdaftar. Yang mengizinkan: baterai rendah, VPN-flag mati (koneksi WS ada berarti jaringan hidup), jam miring.

`rateLimited` menghitung pesan `sent_at` SIM tersebut pada jendela geser 1 mnt / 1 jam / 1 hari (hitung dari indeks `messages_sim_sent_idx`, di-cache dengan penyegaran inkremental dan dihitung ulang dari DB saat start).

### 4.5 Penutupan jeda

Saat pesan dari lane mencapai `sent` / terminal-gagal / `unknown`:

```text
delay := uniform(policy.MinDelay, policy.MaxDelay)
BurstCount++ ; if BurstSize>0 && BurstCount % BurstSize == 0 { delay += BurstPause }
lane.NextEligibleAt = max(lane.NextEligibleAt, now + delay)
UPDATE sims SET next_eligible_at=..., burst_count=...
lane.Inflight--
```

Dengan `max_inflight_per_sim > 1`, jeda diterapkan antar *lease*, bukan antar terminal (konfigurasi lanjutan, default 1).

### 4.6 Policy resolver

Fungsi murni `Resolve(global, device, sim, campaign, message) EffectivePolicy` mengikuti kelompok merge di docs/03 §4: guardrail (max untuk delay, min untuk rate/concurrency/burst, irisan jendela), preferensi (terdekat menang). Dilengkapi uji properti: hasil tidak pernah lebih longgar dari guardrail mana pun; `max ≥ min`; idempoten.

## 5. WS Hub

- Satu `Session` per device: goroutine baca, goroutine tulis (`chan []byte` ukuran 256), pemantau ping. Menulis hanya dari goroutine tulis (tidak ada concurrent write).
- Auth saat upgrade: Bearer → `sha256` → cocokkan `device_credentials.secret_hash` (atau `prev_secret_hash` dalam masa grace) dengan perbandingan constant-time. Gagal: 401 seragam (tanpa membedakan device tak dikenal vs secret salah). Device `revoked` → 401.
- Satu sesi aktif per device: koneksi baru menutup lama dengan `4409`.
- Rate limit pesan masuk: token bucket 50 pesan/dtk/device; pelanggaran → `error{fatal}` + `4429`.
- Backpressure: bila `chan` tulis penuh → putus sesi (device akan reconnect dan rekonsiliasi), jangan memblokir dispatcher.
- Handler pesan: `hello` (rekonsiliasi §6), `heartbeat` (perbarui device/SIM, evaluasi status), `command.ack`, `sms.event` (→ Transition + `event.ack` dibatch maks 100 ms), `sim.report`, `device.command_result`.
- `Hub.Send(deviceID, msg)` mengembalikan `ErrOffline` bila tak ada sesi.

## 6. Rekonsiliasi `hello`

```text
inflight_dev  := payload.inflight
inflight_srv  := SELECT id,status,attempt FROM messages WHERE device_id=$d AND status IN ('assigned','sending')
untuk m dalam inflight_dev:
   srv := cari m.message_id
   tak ada / terminal di server / attempt server lebih baru -> drop[m]
   selain itu -> keep[m]
untuk s dalam inflight_srv:
   s.status='assigned' && tak ada di device -> kirim ulang sms.send (setelah hello.ack)
   s.status='sending'  && tak ada di device -> Transition(unknown, reason=device_lost_state)
balas hello.ack{reconcile}
```

Pesan `unknown` yang kemudian menerima event terminal dari outbox device diperbarui otomatis (transisi `unknown→sent/failed` diizinkan).

## 7. Reaper (leader)

Berjalan tiap 2 dtk (kueri berindeks):

| Tugas | Kondisi | Aksi |
|---|---|---|
| Lease kedaluwarsa | `assigned` & `lease_expires_at < now` | `→ queued` (event `requeued`, alasan `ack_timeout`); hitung kegagalan beruntun SIM |
| Sent timeout | `sending` & `assigned_at + sent_timeout < now` & device tak mengirim event | `→ unknown` (`sent_timeout`) kecuali `retry_on_unknown` → `queued` attempt+1 |
| Kedaluwarsa | `queued/assigned` & `expires_at < now` | `→ expired` |
| Delivery timeout | `sent` & `sent_at + delivery_timeout < now` | tandai `data.delivery_unconfirmed` (status tetap `sent`) |
| Offline detector | `last_seen_at < now - 90s` & status online/degraded | device `→ offline`, publish SSE |
| Kegagalan SIM beruntun | ≥ 3 `ack_timeout` beruntun | SIM `status=no_service` sementara & peringatan |

## 8. Materializer dan auto-pause

- `start` campaign → status `running`/`scheduled`; materializer mengambil `campaign_recipients WHERE materialized_at IS NULL` dalam batch 1.000: render template (error variabel → penerima ditandai gagal impor, bukan dikirim), hitung encoding/segmen, `INSERT messages … ` + `UPDATE materialized_at` dalam satu transaksi. Idempoten berkat indeks unik parsial `messages(recipient_id)`.
- Backpressure: materialisasi dibatasi agar `queued` campaign ≤ 20.000 baris sekaligus (mencegah tabel membengkak untuk campaign besar); batch berikut saat antrean turun.
- `completed`: semua penerima ter-materialisasi dan tidak ada pesan non-terminal.
- Auto-pause: setelah tiap pesan campaign terminal, evaluasi `N` terakhir (`auto_pause.window`): `failed/(failed+sent+delivered) > max_failure_ratio` (hanya kegagalan yang berkaitan dengan SIM/jaringan: `no_service`, `radio_off`, `generic_failure`, `os_rate_limited`, `ack_timeout` — bukan `invalid_destination`) → `paused(auto_failure_ratio)`. `no_capacity`: tidak ada SIM yang memenuhi `sim_ids` online selama 10 mnt.

## 9. Pengodean SMS, normalisasi, template

- **Normalisasi nomor**: libphonenumber-port (`nyaruka/phonenumbers`) dengan `DEFAULT_REGION=ID`; hasil E.164; tolak bila tidak valid atau bukan nomor mobile yang dapat diterima (short code diizinkan hanya dengan flag per SIM, default ditolak).
- **Encoding**: GSM-7 bila semua karakter ada di tabel GSM 03.38 (basic + extension); selain itu UCS-2. Segmen: GSM-7 160 / 153 per bagian; UCS-2 70 / 67; karakter extension GSM (`^ { } \ [ ] ~ | €` dan form feed) menghabiskan 2 septet; karakter di luar BMP (emoji) menghabiskan 2 unit UCS-2. Batas pesan 1.600 karakter.
- **Template**: `{{var}}` (huruf, angka, `_`), variabel hilang → error validasi per penerima; tidak ada eksekusi ekspresi; nilai variabel di-trim dan dibatasi 200 karakter.
- Hasil render disimpan pada `messages.body` (immutable setelah dibuat).

## 10. Autentikasi dan otorisasi (implementasi)

- Middleware urut: request-id → logging → ratelimit → auth → CSRF (mutasi dengan sesi) → RBAC (matriks spec 08 §3) → handler.
- Sesi: token acak 256-bit, cookie `HttpOnly; Secure; SameSite=Lax; Path=/`, hash disimpan di `sessions`, expiry geser 12 jam / absolut 7 hari.
- API key: format `smsc_live_<prefix8>_<secret>`; cari berdasar prefix, bandingkan hash constant-time; perbarui `last_used_at` dengan throttle 1×/menit.
- Header identitas Tailscale hanya dipercaya bila `TRUST_TAILSCALE_HEADERS=true` **dan** `RemoteAddr` termasuk `TRUSTED_PROXIES` (default loopback: Serve di host yang sama; tambahkan alamat sidecar). Identitas klien (`clientKey`) dipakai untuk rate limit login/enroll dan penguncian login per (akun, sumber, tabel `login_failures`).

## 11. Event bus dan SSE

- `events.Publish(topic, payload)` dipanggil setelah commit; broker menyimpan ring buffer 5 menit (`id` monoton in-memory + epoch proses). `Last-Event-ID` dari epoch berbeda → kirim event `resync` agar klien refetch.
- Batching 250 ms per koneksi; heartbeat komentar `: ping` tiap 15 dtk; batas 200 koneksi SSE.
- Filter per peran (viewer: nomor termasking pada payload).

## 12. Shutdown, ketahanan, observabilitas

- SIGTERM: berhenti menerima koneksi baru, hentikan dispatcher (tidak ada lease baru), tutup WS dengan `1012` (device reconnect), tunggu transaksi berjalan ≤ 20 dtk, tutup pool DB.
- Start: migrasi (goose) bila `AUTO_MIGRATE=true`; bangun ulang lane dari DB; reaper memulihkan lease basi.
- Metrik: lihat docs/04 §6; tambah `smsc_transition_total{from,to}`, `smsc_dispatch_wait_seconds`, `smsc_ws_messages_total{type,dir}`.
- Pelacakan: `trace_id` per request dan `message_id`/`device_id` pada log pengiriman; body SMS disensor.
