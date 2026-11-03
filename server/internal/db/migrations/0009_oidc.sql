-- Fase 11: SSO dengan OpenID Connect (authorization code + PKCE).
BEGIN;
ALTER TABLE users
  ADD COLUMN oidc_issuer    text,
  ADD COLUMN oidc_subject   text,
  ADD COLUMN oidc_linked_at timestamptz;
CREATE UNIQUE INDEX users_oidc_uq ON users (oidc_issuer, oidc_subject) WHERE oidc_subject IS NOT NULL;

ALTER TABLE sessions ADD COLUMN sso boolean NOT NULL DEFAULT false;   -- sesi dibuat lewat SSO

-- state alur OIDC: sekali pakai, terikat ke peramban lewat cookie "binder" (hanya hash disimpan)
CREATE TABLE oidc_flows (
  state_hash  bytea PRIMARY KEY,
  binder_hash bytea NOT NULL,
  nonce       text NOT NULL,
  verifier    text NOT NULL,                    -- PKCE code_verifier
  purpose     text NOT NULL CHECK (purpose IN ('login','link')),
  user_id     uuid REFERENCES users(id) ON DELETE CASCADE,   -- untuk purpose=link
  next_path   text,
  expires_at  timestamptz NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX oidc_flows_expires_idx ON oidc_flows (expires_at);
COMMIT;
