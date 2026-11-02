-- 0003: webhook (outbox pengiriman) dan retensi (fase 3).
BEGIN;
ALTER TABLE webhooks ADD COLUMN description text;
ALTER TABLE webhooks ADD COLUMN created_by uuid REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE webhooks ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE webhook_deliveries ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE webhook_deliveries ADD COLUMN failed_at timestamptz;
ALTER TABLE webhook_deliveries ADD COLUMN last_error text;
ALTER TABLE webhook_deliveries ADD COLUMN last_attempt_at timestamptz;
CREATE INDEX webhook_deliveries_hook_idx ON webhook_deliveries (webhook_id, created_at DESC);
CREATE INDEX messages_purge_idx ON messages ((COALESCE(finished_at, delivered_at, sent_at, created_at))) WHERE body IS NOT NULL AND status NOT IN ('queued','assigned','sending');
CREATE INDEX message_events_at_idx ON message_events (at);
COMMIT;
