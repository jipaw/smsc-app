-- Fase 33: kapan hitungan bagian terkirim pesan gagal-parsial disegarkan dengan sms.status (NULL = belum).
BEGIN;
ALTER TABLE messages ADD COLUMN partial_checked_at timestamptz;
COMMIT;
