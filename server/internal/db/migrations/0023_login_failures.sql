-- Penguncian login per (akun, sumber) agar pihak lain tidak dapat mengunci akun dari sumbernya sendiri.
-- users.failed_logins/locked_until tetap ada sebagai jaring pengaman global (ambang lebih tinggi) dan untuk kode 2FA.
BEGIN;
CREATE TABLE login_failures (
  user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  source       text NOT NULL,
  failures     integer NOT NULL DEFAULT 0,
  locked_until timestamptz,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, source)
);
COMMIT;
