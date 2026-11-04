-- Fase 23: jumlah sms.status yang sudah ditanyakan ke device untuk percobaan kirim ini (dibatasi; direset saat penugasan).
BEGIN;
ALTER TABLE messages ADD COLUMN status_queries smallint NOT NULL DEFAULT 0 CHECK (status_queries >= 0);
COMMIT;
