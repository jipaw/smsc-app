-- Fase 24: kemajuan terakhir yang dilaporkan device lewat sms.state (parts_sent + parts_delivered), untuk pesan multi-bagian.
BEGIN;
ALTER TABLE messages ADD COLUMN status_progress integer NOT NULL DEFAULT 0 CHECK (status_progress >= 0);
COMMIT;
