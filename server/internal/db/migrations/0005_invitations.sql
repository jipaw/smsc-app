-- Fase 6: undangan pengguna (tautan sekali pakai) dan hapus pengguna.
BEGIN;
CREATE TABLE invitations (
  id               uuid PRIMARY KEY,
  email            text NOT NULL,
  role             user_role NOT NULL,
  token_hash       bytea NOT NULL UNIQUE,
  created_by       uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz NOT NULL,
  accepted_at      timestamptz,
  accepted_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  revoked_at       timestamptz
);
CREATE INDEX invitations_email_idx ON invitations (lower(email));
COMMIT;
