-- Fase 10: login tanpa password dengan passkey (kredensial discoverable, verifikasi pengguna wajib).
BEGIN;
ALTER TABLE webauthn_credentials ADD COLUMN passkey boolean NOT NULL DEFAULT false;  -- boleh dipakai untuk login tanpa password
ALTER TABLE webauthn_ceremonies ALTER COLUMN user_id DROP NOT NULL;                  -- login passkey: pengguna belum diketahui
ALTER TABLE webauthn_ceremonies DROP CONSTRAINT webauthn_ceremonies_kind_check;
ALTER TABLE webauthn_ceremonies ADD CONSTRAINT webauthn_ceremonies_kind_check CHECK (kind IN ('register','register_passkey','login','passkey_login'));
COMMIT;
