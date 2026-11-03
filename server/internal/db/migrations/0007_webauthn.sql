-- Fase 9: WebAuthn (kunci keamanan / passkey) sebagai faktor kedua.
BEGIN;
CREATE TABLE webauthn_credentials (
  id            uuid PRIMARY KEY,
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id bytea NOT NULL UNIQUE,
  name          text NOT NULL,
  data          jsonb NOT NULL,            -- webauthn.Credential (kunci publik, sign count, flag, transport)
  created_at    timestamptz NOT NULL DEFAULT now(),
  last_used_at  timestamptz
);
CREATE INDEX webauthn_credentials_user_idx ON webauthn_credentials (user_id);

-- sesi upacara (challenge) sekali pakai antara begin dan finish
CREATE TABLE webauthn_ceremonies (
  id         uuid PRIMARY KEY,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind       text NOT NULL CHECK (kind IN ('register','login')),
  data       jsonb NOT NULL,
  name       text,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webauthn_ceremonies_expires_idx ON webauthn_ceremonies (expires_at);
COMMIT;
