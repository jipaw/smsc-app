-- Fase 4: rotasi kredensial dan 2FA (TOTP).
BEGIN;
ALTER TABLE users
  ADD COLUMN totp_secret         bytea,                 -- terenkripsi (AES-GCM); NULL = 2FA mati
  ADD COLUMN totp_pending        bytea,                 -- rahasia yang sedang disiapkan, belum dikonfirmasi
  ADD COLUMN totp_enabled_at     timestamptz,
  ADD COLUMN totp_last_step      bigint NOT NULL DEFAULT 0,  -- langkah 30 dtk terakhir yang dipakai (anti-replay)
  ADD COLUMN password_changed_at timestamptz;

CREATE TABLE user_recovery_codes (
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash  bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  used_at    timestamptz,
  PRIMARY KEY (user_id, code_hash)
);

ALTER TABLE api_keys
  ADD COLUMN prev_key_hash    bytea,
  ADD COLUMN prev_valid_until timestamptz,
  ADD COLUMN rotated_at       timestamptz;

ALTER TABLE webhooks
  ADD COLUMN prev_signing_secret bytea,
  ADD COLUMN prev_valid_until    timestamptz,
  ADD COLUMN secret_rotated_at   timestamptz;

ALTER TABLE device_credentials
  ADD COLUMN pending_secret_hash bytea,
  ADD COLUMN pending_command_id  uuid,
  ADD COLUMN pending_expires_at  timestamptz;
COMMIT;
