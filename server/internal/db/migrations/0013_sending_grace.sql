-- Fase 22: tambahan batas waktu 'sending' (ms) yang diberikan saat leader berganti agar jeda tanpa leader tidak dihitung sebagai sent_timeout.
BEGIN;
ALTER TABLE messages ADD COLUMN sent_grace_ms integer NOT NULL DEFAULT 0 CHECK (sent_grace_ms >= 0);
COMMIT;
