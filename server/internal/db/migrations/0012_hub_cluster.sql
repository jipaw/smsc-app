-- Fase 20: WS Hub multi-instance. Tabel efemeral (UNLOGGED: kosong setelah crash DB; semuanya dapat dibangun ulang).
BEGIN;
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
COMMIT;
