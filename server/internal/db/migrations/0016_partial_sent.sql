-- Fase 25: pesan yang gagal setelah sebagian bagian SMS terkirim (NULL = bukan kegagalan parsial; nilai = jumlah bagian terkirim).
BEGIN;
ALTER TABLE messages ADD COLUMN partial_sent smallint CHECK (partial_sent >= 0);
CREATE INDEX messages_partial_idx ON messages (created_at DESC) WHERE partial_sent IS NOT NULL;
COMMIT;
