-- SMSC Gateway — skema PostgreSQL 16 (v1, snapshot AKHIR = migrasi 0001+0002+0003; dicek oleh server/tests/schema_sync.sh). Sumber kebenaran: docs/03 dan docs/spec/07.
-- ID dibuat oleh aplikasi (UUIDv7). Semua waktu timestamptz (UTC).

BEGIN;

-- ---------- enum ----------
CREATE TYPE device_status    AS ENUM ('pending','online','degraded','offline','revoked');
CREATE TYPE sim_status       AS ENUM ('active','no_service','absent','disabled');
CREATE TYPE message_status   AS ENUM ('queued','assigned','sending','sent','delivered','failed','cancelled','expired','unknown');
CREATE TYPE message_source   AS ENUM ('dashboard','api','campaign');
CREATE TYPE campaign_status  AS ENUM ('draft','scheduled','running','paused','completed','cancelled');
CREATE TYPE user_role        AS ENUM ('admin','operator','viewer');
CREATE TYPE sms_encoding     AS ENUM ('gsm7','ucs2');

-- ---------- identitas & akses ----------
CREATE TABLE users (
  id            uuid PRIMARY KEY,
  email         text NOT NULL,
  password_hash text,
  role          user_role NOT NULL DEFAULT 'viewer',
  tailscale_login text,
  disabled      boolean NOT NULL DEFAULT false,
  failed_logins integer NOT NULL DEFAULT 0,
  locked_until  timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  totp_secret         bytea,
  totp_pending        bytea,
  totp_enabled_at     timestamptz,
  totp_last_step      bigint NOT NULL DEFAULT 0,
  password_changed_at timestamptz,
  oidc_issuer         text,
  oidc_subject        text,
  oidc_linked_at      timestamptz,
  scim_managed        boolean NOT NULL DEFAULT false,
  scim_external_id    text,
  scim_attrs          jsonb NOT NULL DEFAULT '{}',
  scim_updated_at     timestamptz,
  scim_direct_role    user_role
);
CREATE UNIQUE INDEX users_email_uq ON users (lower(email));
CREATE UNIQUE INDEX users_oidc_uq ON users (oidc_issuer, oidc_subject) WHERE oidc_subject IS NOT NULL;
CREATE UNIQUE INDEX users_scim_external_uq ON users (scim_external_id) WHERE scim_external_id IS NOT NULL;
CREATE UNIQUE INDEX users_ts_login_uq ON users (tailscale_login) WHERE tailscale_login IS NOT NULL;

CREATE TABLE user_recovery_codes (
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash  bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  used_at    timestamptz,
  PRIMARY KEY (user_id, code_hash)
);

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

CREATE TABLE email_outbox (
  id              uuid PRIMARY KEY,
  kind            text NOT NULL,
  to_email        text NOT NULL,
  subject         text NOT NULL,
  body            bytea,
  invitation_id   uuid REFERENCES invitations(id) ON DELETE CASCADE,
  created_by      uuid REFERENCES users(id) ON DELETE SET NULL,
  status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sent','failed','cancelled')),
  attempts        integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error      text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  sent_at         timestamptz
);
CREATE INDEX email_outbox_due_idx ON email_outbox (next_attempt_at) WHERE status = 'pending';
CREATE INDEX email_outbox_invitation_idx ON email_outbox (invitation_id);

CREATE TABLE webauthn_credentials (
  id            uuid PRIMARY KEY,
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id bytea NOT NULL UNIQUE,
  name          text NOT NULL,
  data          jsonb NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  last_used_at  timestamptz,
  passkey       boolean NOT NULL DEFAULT false
);
CREATE INDEX webauthn_credentials_user_idx ON webauthn_credentials (user_id);

CREATE TABLE webauthn_ceremonies (
  id         uuid PRIMARY KEY,
  user_id    uuid REFERENCES users(id) ON DELETE CASCADE,
  kind       text NOT NULL CHECK (kind IN ('register','register_passkey','login','passkey_login')),
  data       jsonb NOT NULL,
  name       text,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webauthn_ceremonies_expires_idx ON webauthn_ceremonies (expires_at);

CREATE TABLE oidc_flows (
  state_hash  bytea PRIMARY KEY,
  binder_hash bytea NOT NULL,
  nonce       text NOT NULL,
  verifier    text NOT NULL,
  purpose     text NOT NULL CHECK (purpose IN ('login','link')),
  user_id     uuid REFERENCES users(id) ON DELETE CASCADE,
  next_path   text,
  expires_at  timestamptz NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX oidc_flows_expires_idx ON oidc_flows (expires_at);

CREATE TABLE scim_groups (
  id           uuid PRIMARY KEY,
  display_name text NOT NULL,
  external_id  text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX scim_groups_name_uq ON scim_groups (lower(display_name));
CREATE UNIQUE INDEX scim_groups_ext_uq  ON scim_groups (external_id) WHERE external_id IS NOT NULL;

CREATE TABLE scim_group_members (
  group_id uuid NOT NULL REFERENCES scim_groups(id) ON DELETE CASCADE,
  user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  PRIMARY KEY (group_id, user_id)
);
CREATE INDEX scim_group_members_user_idx ON scim_group_members (user_id);

CREATE TABLE scim_tokens (
  id           uuid PRIMARY KEY,
  name         text NOT NULL,
  token_hash   bytea NOT NULL UNIQUE,
  created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  revoked_at   timestamptz
);

CREATE TABLE sessions (
  id          uuid PRIMARY KEY,
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash  bytea NOT NULL UNIQUE,
  csrf_token  text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  expires_at  timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  sso         boolean NOT NULL DEFAULT false
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

CREATE TABLE api_keys (
  id           uuid PRIMARY KEY,
  name         text NOT NULL,
  prefix       text NOT NULL,
  key_hash     bytea NOT NULL UNIQUE,
  scopes       text[] NOT NULL DEFAULT '{}',
  created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  expires_at   timestamptz,
  revoked_at   timestamptz,
  last_used_at timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  prev_key_hash    bytea,
  prev_valid_until timestamptz,
  rotated_at       timestamptz
);

-- ---------- policy ----------
CREATE TABLE dispatch_policies (
  id                   uuid PRIMARY KEY,
  name                 text NOT NULL UNIQUE,
  is_default           boolean NOT NULL DEFAULT false,
  min_delay_ms         integer CHECK (min_delay_ms >= 0),
  max_delay_ms         integer CHECK (max_delay_ms >= 0),
  burst_size           integer CHECK (burst_size >= 1),
  burst_pause_ms       integer CHECK (burst_pause_ms >= 0),
  max_inflight_per_sim integer CHECK (max_inflight_per_sim >= 1),
  rate_per_minute      integer CHECK (rate_per_minute >= 1),
  rate_per_hour        integer CHECK (rate_per_hour >= 1),
  rate_per_day         integer CHECK (rate_per_day >= 1),
  send_window          jsonb,
  max_attempts         integer CHECK (max_attempts BETWEEN 1 AND 10),
  retry_backoff_ms     integer CHECK (retry_backoff_ms >= 1000),
  retry_backoff_max_ms integer,
  message_ttl_seconds  integer CHECK (message_ttl_seconds >= 30),
  track_delivery       boolean,
  ack_timeout_ms       integer CHECK (ack_timeout_ms > 0),
  sent_timeout_ms      integer CHECK (sent_timeout_ms > 0),
  retry_on_unknown     boolean,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  CHECK (min_delay_ms IS NULL OR max_delay_ms IS NULL OR max_delay_ms >= min_delay_ms),
  -- policy default harus lengkap (tidak boleh null) supaya merge selalu terdefinisi
  CHECK (NOT is_default OR (
    min_delay_ms IS NOT NULL AND max_delay_ms IS NOT NULL AND burst_size IS NOT NULL AND
    burst_pause_ms IS NOT NULL AND max_inflight_per_sim IS NOT NULL AND max_attempts IS NOT NULL AND
    retry_backoff_ms IS NOT NULL AND retry_backoff_max_ms IS NOT NULL AND message_ttl_seconds IS NOT NULL AND
    track_delivery IS NOT NULL AND ack_timeout_ms IS NOT NULL AND sent_timeout_ms IS NOT NULL AND
    retry_on_unknown IS NOT NULL AND send_window IS NOT NULL))
);
CREATE UNIQUE INDEX dispatch_policies_one_default ON dispatch_policies ((true)) WHERE is_default;

-- ---------- device & SIM ----------
CREATE TABLE devices (
  id              uuid PRIMARY KEY,
  name            text NOT NULL,
  status          device_status NOT NULL DEFAULT 'pending',
  status_reason   text,
  enabled         boolean NOT NULL DEFAULT true,
  manufacturer    text,
  model           text,
  android_version text,
  sdk_int         integer CHECK (sdk_int IS NULL OR sdk_int >= 29),
  app_version     text,
  install_id      uuid NOT NULL UNIQUE,
  capabilities    jsonb NOT NULL DEFAULT '{}',
  battery_level   smallint CHECK (battery_level BETWEEN 0 AND 100),
  charging        boolean,
  network_type    text,
  tailscale_ip    inet,
  policy_id       uuid REFERENCES dispatch_policies(id) ON DELETE SET NULL,
  tags            text[] NOT NULL DEFAULT '{}',
  config_version  integer NOT NULL DEFAULT 1,
  last_seen_at    timestamptz,
  connected_since timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  revoked_at      timestamptz
);
CREATE INDEX devices_status_idx ON devices (status);

CREATE TABLE device_credentials (
  device_id         uuid PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
  secret_hash       bytea NOT NULL,
  prev_secret_hash  bytea,
  prev_valid_until  timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  rotated_at        timestamptz,
  pending_secret_hash bytea,
  pending_command_id  uuid,
  pending_expires_at  timestamptz
);

CREATE TABLE device_enrollments (
  id                    uuid PRIMARY KEY,
  token_hash            bytea NOT NULL UNIQUE,
  short_code_hash       bytea NOT NULL UNIQUE,
  device_name           text,
  policy_id             uuid REFERENCES dispatch_policies(id) ON DELETE SET NULL,
  created_by            uuid REFERENCES users(id) ON DELETE SET NULL,
  failed_attempts       smallint NOT NULL DEFAULT 0,
  expires_at            timestamptz NOT NULL,
  consumed_at           timestamptz,
  consumed_by_device_id uuid REFERENCES devices(id) ON DELETE SET NULL,
  revoked_at            timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sims (
  id               uuid PRIMARY KEY,
  device_id        uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  slot_index       smallint NOT NULL CHECK (slot_index >= 0),
  subscription_id  integer,
  carrier_name     text,
  display_name     text,
  phone_number     text,
  is_esim          boolean NOT NULL DEFAULT false,
  label            text,
  enabled          boolean NOT NULL DEFAULT true,
  status           sim_status NOT NULL DEFAULT 'active',
  signal_level     smallint CHECK (signal_level BETWEEN 0 AND 4),
  registered       boolean,
  roaming          boolean,
  policy_id        uuid REFERENCES dispatch_policies(id) ON DELETE SET NULL,
  next_eligible_at timestamptz,
  burst_count      integer NOT NULL DEFAULT 0,
  last_seen_at     timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (device_id, slot_index)
);
CREATE INDEX sims_ready_idx ON sims (next_eligible_at) WHERE enabled AND status = 'active';

-- ---------- konten ----------
CREATE TABLE templates (
  id         uuid PRIMARY KEY,
  name       text NOT NULL UNIQUE,
  body       text NOT NULL CHECK (char_length(body) <= 1600),
  variables  text[] NOT NULL DEFAULT '{}',
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE campaigns (
  id           uuid PRIMARY KEY,
  name         text NOT NULL,
  description  text,
  status       campaign_status NOT NULL DEFAULT 'draft',
  pause_reason text CHECK (pause_reason IN ('user','auto_failure_ratio','no_capacity')),
  body         text,
  template_id  uuid REFERENCES templates(id) ON DELETE RESTRICT,
  sim_ids      uuid[] NOT NULL DEFAULT '{}',
  policy_id    uuid REFERENCES dispatch_policies(id) ON DELETE SET NULL,
  priority     smallint NOT NULL DEFAULT 3 CHECK (priority BETWEEN 0 AND 9),
  start_at     timestamptz,
  auto_pause   jsonb NOT NULL DEFAULT '{"enabled":true,"window":50,"max_failure_ratio":0.5}',
  created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  started_at   timestamptz,
  finished_at  timestamptz,
  idempotency_key text,
  eval_since   timestamptz,   -- baseline auto-pause: hanya percobaan setelah waktu ini yang dievaluasi (di-set saat start/resume)
  CHECK ((body IS NOT NULL) <> (template_id IS NOT NULL))
);
CREATE INDEX campaigns_status_idx ON campaigns (status);
CREATE UNIQUE INDEX campaigns_idem_uq ON campaigns (created_by, idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE TABLE campaign_recipients (
  id              uuid PRIMARY KEY,
  campaign_id     uuid NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  row_no          integer NOT NULL,
  to_raw          text NOT NULL,
  to_e164         text NOT NULL,
  vars            jsonb NOT NULL DEFAULT '{}',
  client_ref      text,
  materialized_at timestamptz
);
CREATE UNIQUE INDEX campaign_recipients_dedupe ON campaign_recipients (campaign_id, to_e164);
CREATE INDEX campaign_recipients_pending ON campaign_recipients (campaign_id, row_no) WHERE materialized_at IS NULL;

CREATE TABLE suppressions (
  id          uuid PRIMARY KEY,
  number_e164 text NOT NULL UNIQUE,
  reason      text,
  created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- ---------- antrean + transaksi ----------
CREATE TABLE messages (
  id               uuid PRIMARY KEY,
  source           message_source NOT NULL,
  campaign_id      uuid REFERENCES campaigns(id) ON DELETE SET NULL,
  recipient_id     uuid REFERENCES campaign_recipients(id) ON DELETE SET NULL,
  client_ref       text,
  idempotency_key  text,
  owner_id         uuid,                       -- users.id atau api_keys.id (pembuat)
  to_e164          text NOT NULL,
  body             text,                       -- dapat di-NULL-kan oleh retensi
  encoding         sms_encoding NOT NULL,
  parts            smallint NOT NULL CHECK (parts >= 1),
  status           message_status NOT NULL DEFAULT 'queued',
  attempt          smallint NOT NULL DEFAULT 0,
  max_attempts     smallint NOT NULL DEFAULT 3,
  error_code       text,
  error_detail     text,
  priority         smallint NOT NULL DEFAULT 5 CHECK (priority BETWEEN 0 AND 9),
  route_sim_ids    uuid[] NOT NULL DEFAULT '{}',
  route_device_ids uuid[] NOT NULL DEFAULT '{}',
  policy_id        uuid REFERENCES dispatch_policies(id) ON DELETE SET NULL,
  device_id        uuid REFERENCES devices(id) ON DELETE SET NULL,
  sim_id           uuid REFERENCES sims(id) ON DELETE SET NULL,
  not_before       timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz NOT NULL,
  lease_expires_at timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  assigned_at      timestamptz,
  sent_at          timestamptz,
  delivered_at     timestamptz,
  finished_at      timestamptz,
  sent_grace_ms    integer NOT NULL DEFAULT 0 CHECK (sent_grace_ms >= 0),
  status_queries   smallint NOT NULL DEFAULT 0 CHECK (status_queries >= 0),
  status_progress  integer NOT NULL DEFAULT 0 CHECK (status_progress >= 0),
  partial_sent     smallint CHECK (partial_sent >= 0),
  partial_checked_at timestamptz,
  CHECK (status <> 'assigned' OR (sim_id IS NOT NULL AND lease_expires_at IS NOT NULL))
);
CREATE INDEX messages_dispatch_idx   ON messages (priority DESC, not_before, id) WHERE status = 'queued';
CREATE INDEX messages_lease_idx      ON messages (lease_expires_at) WHERE status = 'assigned';
CREATE INDEX messages_inflight_idx   ON messages (device_id, status) WHERE status IN ('assigned','sending');
CREATE INDEX messages_campaign_idx   ON messages (campaign_id, status);
CREATE INDEX messages_created_idx    ON messages (created_at DESC, id DESC);
CREATE INDEX messages_partial_idx ON messages (created_at DESC) WHERE partial_sent IS NOT NULL;
CREATE INDEX messages_to_idx         ON messages (to_e164, created_at DESC);
CREATE INDEX messages_sim_sent_idx   ON messages (sim_id, sent_at) WHERE sent_at IS NOT NULL;
CREATE INDEX messages_expiry_idx     ON messages (expires_at) WHERE status IN ('queued','assigned');
CREATE INDEX messages_unknown_idx    ON messages (assigned_at) WHERE status = 'sending';
CREATE INDEX messages_purge_idx ON messages ((COALESCE(finished_at, delivered_at, sent_at, created_at))) WHERE body IS NOT NULL AND status NOT IN ('queued','assigned','sending');
CREATE UNIQUE INDEX messages_idem_uq ON messages (owner_id, idempotency_key, to_e164) WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX messages_recipient_uq ON messages (recipient_id) WHERE recipient_id IS NOT NULL;
CREATE UNIQUE INDEX messages_api_ref_uq ON messages (client_ref) WHERE source = 'api' AND client_ref IS NOT NULL;

CREATE TABLE message_events (
  id              uuid PRIMARY KEY,
  message_id      uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  attempt         smallint NOT NULL,
  type            text NOT NULL,
  at              timestamptz NOT NULL DEFAULT now(),
  actor           text NOT NULL CHECK (actor IN ('system','device','user','api')),
  sim_id          uuid REFERENCES sims(id) ON DELETE SET NULL,
  error_code      text,
  data            jsonb NOT NULL DEFAULT '{}',
  device_event_id uuid UNIQUE
);
CREATE INDEX message_events_msg_idx ON message_events (message_id, at);
CREATE INDEX message_events_at_idx ON message_events (at);

-- ---------- fase 2 & operasional ----------
CREATE TABLE webhooks (
  id             uuid PRIMARY KEY,
  url            text NOT NULL,
  events         text[] NOT NULL,
  signing_secret bytea NOT NULL,
  enabled        boolean NOT NULL DEFAULT true,
  created_at     timestamptz NOT NULL DEFAULT now(),
  description    text,
  created_by     uuid REFERENCES users(id) ON DELETE SET NULL,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  prev_signing_secret bytea,
  prev_valid_until    timestamptz,
  secret_rotated_at   timestamptz
);

CREATE TABLE webhook_deliveries (
  id           uuid PRIMARY KEY,
  webhook_id   uuid NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
  event_type   text NOT NULL,
  payload      jsonb NOT NULL,
  attempts     smallint NOT NULL DEFAULT 0,
  next_attempt_at timestamptz,
  delivered_at timestamptz,
  last_status  integer,
  created_at   timestamptz NOT NULL DEFAULT now(),
  failed_at    timestamptz,
  last_error   text,
  last_attempt_at timestamptz
);
CREATE INDEX webhook_deliveries_due ON webhook_deliveries (next_attempt_at) WHERE delivered_at IS NULL;
CREATE INDEX webhook_deliveries_hook_idx ON webhook_deliveries (webhook_id, created_at DESC);

CREATE TABLE audit_logs (
  id          uuid PRIMARY KEY,
  at          timestamptz NOT NULL DEFAULT now(),
  actor       text NOT NULL,
  action      text NOT NULL,
  target_type text,
  target_id   text,
  data        jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_logs_at_idx ON audit_logs (at DESC);

CREATE TABLE settings (
  key        text PRIMARY KEY,
  value      jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- instance mana yang memegang sesi WS tiap device (satu baris per device; heartbeat_at dijaga instance pemilik)
CREATE UNLOGGED TABLE device_sessions (
  device_id    uuid PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
  instance_id  uuid NOT NULL,
  session_id   uuid NOT NULL,
  connected_at timestamptz NOT NULL DEFAULT now(),
  heartbeat_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX device_sessions_instance_idx ON device_sessions (instance_id);

-- perintah antar-instance (send/request/reply/close); NOTIFY hanya membangunkan, isinya selalu dibaca dari sini
CREATE UNLOGGED TABLE hub_commands (
  id             bigserial PRIMARY KEY,
  to_instance    uuid NOT NULL,
  device_id      uuid NOT NULL,
  kind           text NOT NULL CHECK (kind IN ('send','request','reply','close')),
  env            jsonb,
  reply_instance uuid,
  code           integer,
  reason         text,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX hub_commands_to_idx ON hub_commands (to_instance, id);

-- id envelope sms.send -> pesan, agar command.ack yang tiba di instance lain tetap dapat dikaitkan
CREATE UNLOGGED TABLE cmd_refs (
  env_id     text PRIMARY KEY,
  message_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX cmd_refs_created_idx ON cmd_refs (created_at);

CREATE TABLE saved_views (
  id         uuid PRIMARY KEY,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
  filter     jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  shared     boolean NOT NULL DEFAULT false,
  CONSTRAINT saved_views_shared_no_number CHECK (NOT shared OR COALESCE(filter->>'to', '') = '')
);
CREATE UNIQUE INDEX saved_views_user_name_uq ON saved_views (user_id, lower(name));
CREATE INDEX saved_views_shared_idx ON saved_views (lower(name)) WHERE shared;

CREATE TABLE notifications (
  id         uuid PRIMARY KEY,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind       text NOT NULL,
  data       jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  read_at    timestamptz
);
CREATE INDEX notifications_user_idx ON notifications (user_id, created_at DESC);
CREATE INDEX notifications_unread_idx ON notifications (user_id) WHERE read_at IS NULL;

CREATE TABLE user_prefs (
  user_id             uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  email_notifications boolean NOT NULL DEFAULT true,
  updated_at          timestamptz NOT NULL DEFAULT now()
);

-- Seed: policy default sesuai docs/03 §4
INSERT INTO dispatch_policies (id, name, is_default, min_delay_ms, max_delay_ms, burst_size, burst_pause_ms,
  max_inflight_per_sim, rate_per_minute, rate_per_hour, rate_per_day, send_window, max_attempts,
  retry_backoff_ms, retry_backoff_max_ms, message_ttl_seconds, track_delivery, ack_timeout_ms,
  sent_timeout_ms, retry_on_unknown)
VALUES ('00000000-0000-7000-8000-000000000001', 'Default', true, 3000, 8000, 20, 60000, 1, 10, 300, 2000,
  '{"timezone":"Asia/Jakarta","windows":[{"days":[1,2,3,4,5,6,7],"start":"08:00","end":"20:00"}]}',
  3, 30000, 600000, 86400, true, 15000, 120000, false);
INSERT INTO settings (key, value) VALUES
  ('retention', '{"body_retention_days":90,"event_retention_days":180}'),
  ('default_region', '"ID"'),
  ('delivery_timeout_seconds', '86400'),
  ('offline_after_seconds', '90');

COMMIT;
