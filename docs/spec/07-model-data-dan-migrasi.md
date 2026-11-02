# Spec 07 — Model Data dan Migrasi

DDL final v1: [`contracts/schema.sql`](../../contracts/schema.sql) (PostgreSQL 16, **sudah diuji**: dimuat bersih, 18 tabel, dan constraint kunci diverifikasi). Model logis dan ERD ada di `docs/03`; dokumen ini menetapkan keputusan fisik, migrasi, retensi, dan sizing. Bila berbeda dari `docs/03`, **DDL yang menang**.

## 1. Perbedaan DDL terhadap model logis (`docs/03`)

| Hal | Keputusan akhir |
|---|---|
| Pemilik pesan | `messages.owner_id` (uuid, merujuk `users.id` atau `api_keys.id`) menggantikan dua kolom `api_key_id`/`user_id`. |
| Idempotensi | Unik `(owner_id, idempotency_key, to_e164)` — kunci berlaku per penerima sehingga permintaan batch aman diulang. |
| Materialisasi campaign | Unik `messages(recipient_id)` (parsial) menjamin satu penerima → satu pesan walau materializer terulang. |
| Kunci anti-ganda API | Unik parsial `messages(client_ref) WHERE source='api'`. |
| Policy default | Satu baris `is_default` (indeks unik parsial) **wajib lengkap** (CHECK); policy lain boleh parsial. |
| `messages.body` | Nullable agar retensi dapat menghapus isi. |
| Counter campaign | Tidak disimpan; dihitung dari `messages` (agregat per `campaign_id,status`, di-cache 2 dtk). Menghindari drift. |

## 2. Keputusan fisik

- **Tipe:** `uuid` (UUIDv7 dibuat aplikasi), `timestamptz`, `enum` untuk status (tambah nilai via `ALTER TYPE … ADD VALUE`, tidak pernah dihapus), `jsonb` untuk `capabilities`, `send_window`, `auto_pause`, `data` event.
- **Hash kredensial:** `bytea` berisi SHA-256 (token berentropi tinggi, bukan password) — cukup dan memungkinkan lookup berindeks. Password pengguna: argon2id di `users.password_hash` (text, format PHC).
- **FK:** `ON DELETE CASCADE` hanya untuk anak murni (sims, credentials, events, recipients). Referensi historis memakai `SET NULL` agar transaksi tidak hilang saat entitas dihapus (mis. device dihapus permanen).
- **Soft-delete:** device tidak dihapus keras secara default (`status='revoked'`, `revoked_at`); SIM menjadi `absent`.
- **Zona waktu:** semua `timestamptz` disimpan UTC; zona hanya di `send_window.timezone` (nama IANA).

## 3. Pembenaran indeks

| Indeks | Kueri yang dilayani | Catatan terukur |
|---|---|---|
| `messages_dispatch_idx (priority DESC, not_before, id) WHERE status='queued'` | Pengambilan dispatcher | `EXPLAIN` memakai *Index Only Scan*; indeks kecil karena hanya baris `queued` |
| `messages_lease_idx (lease_expires_at) WHERE status='assigned'` | Reaper lease | Sangat kecil |
| `messages_unknown_idx (assigned_at) WHERE status='sending'` | Reaper `sent_timeout` | – |
| `messages_expiry_idx (expires_at) WHERE status IN ('queued','assigned')` | Reaper kedaluwarsa | – |
| `messages_inflight_idx (device_id,status)` parsial | Rekonsiliasi `hello`, device-lost | – |
| `messages_campaign_idx (campaign_id,status)` | Counter, filter | Pertimbangkan `INCLUDE` bila agregat lambat |
| `messages_created_idx (created_at DESC,id DESC)` | Daftar transaksi (cursor) | Filter lain memakai kombinasi di aplikasi; tambahkan indeks komposit sesuai pola kueri hasil pengukuran |
| `messages_to_idx (to_e164, created_at DESC)` | Pencarian nomor (exact/prefix dengan `text_pattern_ops` bila perlu) | Bila pencarian prefix umum, ganti ke `text_pattern_ops` |
| `messages_sim_sent_idx (sim_id, sent_at)` | Rate limit per SIM | – |

Aturan: tidak menambah indeks tanpa kueri nyata + `EXPLAIN`; revisi indeks lewat migrasi tersendiri.

## 4. Kueri pelaporan penting

```sql
-- counter campaign
SELECT status, count(*) FROM messages WHERE campaign_id = $1 GROUP BY status;

-- timeseries per jam
SELECT date_trunc('hour', sent_at) AS t,
       count(*) FILTER (WHERE status IN ('sent','delivered')) AS sent,
       count(*) FILTER (WHERE status = 'delivered')          AS delivered,
       count(*) FILTER (WHERE status = 'failed')             AS failed
FROM messages WHERE created_at >= $1 AND created_at < $2 GROUP BY 1 ORDER BY 1;
```

Untuk agregat dashboard skala besar: tabel rollup `stats_hourly` (fase 3) diisi job per jam; v1 cukup kueri langsung + cache 5 dtk.

## 5. Migrasi

- Alat: **goose** (SQL). Berkas `NNNN_deskripsi.sql` dengan `-- +goose Up/Down`. `0001_init.sql` = isi `contracts/schema.sql`. `AUTO_MIGRATE` hanya untuk lingkungan non-produksi; produksi menjalankan `smscd migrate up` saat deploy.
- Pola **expand → migrate → contract** untuk perubahan merusak:
  1. Expand: tambah kolom/tabel nullable atau dengan default; rilis kode yang menulis keduanya.
  2. Migrate: backfill bertahap (batch ≤ 10.000 baris, tanpa mengunci lama).
  3. Contract: rilis kode yang hanya memakai yang baru, hapus yang lama di rilis berikutnya.
- Dilarang di produksi tanpa rencana: `ALTER TABLE … ADD COLUMN … DEFAULT <volatile>`, `CREATE INDEX` tanpa `CONCURRENTLY` pada `messages`, perubahan tipe kolom besar.
- Setiap migrasi diuji: up pada salinan skema lama + up/down/up pada DB kosong di CI; skema hasil di-*diff* terhadap `contracts/schema.sql` (sumber snapshot) agar tidak menyimpang.
- Enum: nilai baru lewat `ALTER TYPE … ADD VALUE` di migrasi terpisah (tidak boleh dipakai dalam transaksi yang sama).

## 6. Retensi

| Data | Aturan default | Mekanisme |
|---|---|---|
| `messages.body` | Dikosongkan setelah 90 hari sejak `finished_at` | Job harian, batch 5.000 (`UPDATE … SET body=NULL`) |
| `message_events` | Dihapus setelah 180 hari | Job harian `DELETE … LIMIT` via CTE |
| `messages` (metadata) | Dipertahankan; arsip/partisi bila > 10 juta baris | Partisi per bulan `created_at` (migrasi terencana) |
| `sessions` kedaluwarsa | Hapus harian | – |
| `device_enrollments` terpakai/kedaluwarsa | Hapus setelah 30 hari | – |
| `webhook_deliveries` | 30 hari | – |
| `audit_logs` | Tidak dihapus otomatis (hanya arsip manual) | – |

Nilai dapat diubah di `settings.retention`. Penghapusan dicatat ke audit (jumlah baris), bukan isinya.

## 7. Sizing

| Objek | Perkiraan per baris | 1 juta pesan/bulan |
|---|---|---|
| `messages` (+indeks) | ±0,9 KB | ±0,9 GB |
| `message_events` (±5/pesan, +indeks) | ±0,25 KB/event | ±1,2 GB |
| Total | – | ±2 GB/bulan sebelum retensi |

Setelah retensi body/event, pertumbuhan jangka panjang ±0,4 GB per juta pesan (metadata). Disarankan disk ≥ 50 GB dan alert pada 70%. Pada ±10 juta baris `messages`, evaluasi partisi.

## 8. Backup dan pemulihan

- Harian `pg_dump -Fc` (retensi 14 hari) + WAL archiving opsional untuk RPO menit.
- `device_credentials`/`api_keys` tersimpan sebagai hash → backup tidak membocorkan secret; `settings`/`SESSION_SECRET` di luar DB (env).
- Uji pulih bulanan ke instance sementara; kriteria lulus: `smscd` start, dispatcher membangun lane, tidak ada lease basi yang menimbulkan pengiriman ganda (pesan `assigned/sending` pasca-pulih diproses reaper/rekonsiliasi — operator diberi tahu bahwa pesan `sending` yang tidak diketahui akan menjadi `unknown`).

## 9. Seed awal

Policy `Default` (docs/03 §4), `settings` (retensi, region `ID`, `delivery_timeout_seconds`, `offline_after_seconds`). Pengguna admin pertama dibuat lewat `smscd admin create --email … ` (password acak dicetak sekali) — bukan seed SQL.
