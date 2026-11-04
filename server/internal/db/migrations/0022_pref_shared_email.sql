-- Fase 42: preferensi email untuk tampilan yang dibagikan (opt-in; default mati karena volume N penerima).
BEGIN;
ALTER TABLE user_prefs ADD COLUMN email_shared_views boolean NOT NULL DEFAULT false;
COMMIT;
