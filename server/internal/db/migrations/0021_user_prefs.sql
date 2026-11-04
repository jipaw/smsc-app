-- Fase 41: preferensi pengguna. Baris tidak ada = default (email_notifications = true).
BEGIN;
CREATE TABLE user_prefs (
  user_id             uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  email_notifications boolean NOT NULL DEFAULT true,
  updated_at          timestamptz NOT NULL DEFAULT now()
);
COMMIT;
