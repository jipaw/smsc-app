-- Fase 38: tampilan (filter) transaksi tersimpan per pengguna.
BEGIN;
CREATE TABLE saved_views (
  id         uuid PRIMARY KEY,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
  filter     jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX saved_views_user_name_uq ON saved_views (user_id, lower(name));
COMMIT;
