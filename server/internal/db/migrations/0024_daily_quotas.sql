-- Kuota harian (satuan: bagian SMS) per API key dan per pengguna. NULL = ikuti default env, 0 = tanpa batas.
BEGIN;
ALTER TABLE api_keys ADD COLUMN daily_quota integer CHECK (daily_quota >= 0);
ALTER TABLE users ADD COLUMN daily_quota integer CHECK (daily_quota >= 0);
CREATE TABLE quota_usage (
  subject text NOT NULL CHECK (subject ~ '^(key|user):'),
  day     date NOT NULL,
  used    integer NOT NULL DEFAULT 0,
  PRIMARY KEY (subject, day)
);
ALTER TABLE campaigns DROP CONSTRAINT campaigns_pause_reason_check;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_pause_reason_check CHECK (pause_reason IN ('user','auto_failure_ratio','no_capacity','quota'));
COMMIT;
