-- Fase 8: pengiriman email (undangan) lewat SMTP dengan outbox.
BEGIN;
CREATE TABLE email_outbox (
  id              uuid PRIMARY KEY,
  kind            text NOT NULL,
  to_email        text NOT NULL,
  subject         text NOT NULL,
  body            bytea,                         -- terenkripsi (berisi tautan rahasia); NULL setelah terkirim / gagal final / dibatalkan
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
COMMIT;
