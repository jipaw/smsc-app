-- Fase 12: SCIM 2.0 provisioning (RFC 7643/7644) — IdP mendorong pengguna ke gateway.
BEGIN;
ALTER TABLE users
  ADD COLUMN scim_managed     boolean NOT NULL DEFAULT false,   -- hanya pengguna ini yang terlihat/dapat diubah oleh SCIM
  ADD COLUMN scim_external_id text,
  ADD COLUMN scim_attrs       jsonb NOT NULL DEFAULT '{}',      -- displayName, name.* (diteruskan bolak-balik, tidak dipakai aplikasi)
  ADD COLUMN scim_updated_at  timestamptz;
CREATE UNIQUE INDEX users_scim_external_uq ON users (scim_external_id) WHERE scim_external_id IS NOT NULL;

CREATE TABLE scim_tokens (
  id           uuid PRIMARY KEY,
  name         text NOT NULL,
  token_hash   bytea NOT NULL UNIQUE,
  created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  revoked_at   timestamptz
);
COMMIT;
