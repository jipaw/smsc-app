-- 0002: dukungan campaign fase 2 (idempotensi pembuatan, baseline evaluasi auto-pause).
BEGIN;
ALTER TABLE campaigns ADD COLUMN idempotency_key text;
ALTER TABLE campaigns ADD COLUMN eval_since timestamptz;
CREATE UNIQUE INDEX campaigns_idem_uq ON campaigns (created_by, idempotency_key) WHERE idempotency_key IS NOT NULL;
COMMIT;
