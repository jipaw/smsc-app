package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jipaw/smsc-app/server/internal/sms"
)

type RetentionRun struct {
	At                 time.Time `json:"at"`
	BodiesPurged       int       `json:"bodies_purged"`
	EventsDeleted      int       `json:"events_deleted"`
	RecipientsScrubbed int       `json:"recipients_scrubbed"`
	SessionsDeleted    int       `json:"sessions_deleted"`
	EnrollmentsDeleted int       `json:"enrollments_deleted"`
	DeliveriesDeleted  int       `json:"deliveries_deleted"`
	InvitationsDeleted int       `json:"invitations_deleted"`
}

type RetentionSettings struct {
	BodyRetentionDays  int `json:"body_retention_days"`
	EventRetentionDays int `json:"event_retention_days"`
	// undangan yang sudah selesai (diterima/dicabut/kedaluwarsa) dihapus setelah sekian hari; undangan tertunda tak pernah dihapus
	InvitationRetentionDays int           `json:"invitation_retention_days"`
	LastRun                 *RetentionRun `json:"last_run"`
}

const retentionBatch = 5000

func (s *Service) GetRetention(ctx context.Context) (RetentionSettings, error) {
	r := RetentionSettings{BodyRetentionDays: 90, EventRetentionDays: 180, InvitationRetentionDays: 90}
	var raw []byte
	if err := s.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE key='retention'`).Scan(&raw); err == nil {
		_ = json.Unmarshal(raw, &r)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return r, err
	}
	var last []byte
	if err := s.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE key='retention_last_run'`).Scan(&last); err == nil {
		var run RetentionRun
		if json.Unmarshal(last, &run) == nil {
			r.LastRun = &run
		}
	}
	return r, nil
}

// invites nil = pertahankan nilai saat ini.
func (s *Service) SetRetention(ctx context.Context, body, events int, invites *int, actor string) (RetentionSettings, error) {
	cur, err := s.GetRetention(ctx)
	if err != nil {
		return RetentionSettings{}, err
	}
	inv := cur.InvitationRetentionDays
	if invites != nil {
		inv = *invites
	}
	var errs []FieldError
	if body < 0 || body > 3650 {
		errs = append(errs, FieldError{Field: "body_retention_days", Message: "0..3650 (0 = hapus segera setelah pesan selesai)"})
	}
	if events < 1 || events > 3650 {
		errs = append(errs, FieldError{Field: "event_retention_days", Message: "1..3650"})
	}
	if inv < 1 || inv > 3650 {
		errs = append(errs, FieldError{Field: "invitation_retention_days", Message: "1..3650"})
	}
	if len(errs) > 0 {
		return RetentionSettings{}, Validation(errs...)
	}
	val, _ := json.Marshal(map[string]int{"body_retention_days": body, "event_retention_days": events, "invitation_retention_days": inv})
	if _, err := s.Pool.Exec(ctx, `INSERT INTO settings(key,value) VALUES ('retention',$1) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, val); err != nil {
		return RetentionSettings{}, err
	}
	s.Audit(ctx, actor, "retention.update", "settings", "retention", map[string]any{"body_retention_days": body, "event_retention_days": events, "invitation_retention_days": inv})
	return s.GetRetention(ctx)
}

// RunRetention: job berkala (leader). Run pertama ditunda singkat agar tidak bersaing dengan startup.
func (s *Service) RunRetention(ctx context.Context) {
	first := time.NewTimer(minDur(s.Cfg.RetentionInterval, 30*time.Second))
	defer first.Stop()
	t := time.NewTicker(s.Cfg.RetentionInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		case <-t.C:
		}
		if _, err := s.RetentionOnce(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("retensi gagal", "err", err)
		}
	}
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// batchExec menjalankan statement ber-LIMIT berulang sampai tidak ada baris tersisa; mengembalikan total baris.
func (s *Service) batchExec(ctx context.Context, sql string, args ...any) (int, error) {
	total := 0
	for ctx.Err() == nil {
		tag, err := s.Pool.Exec(ctx, sql, args...)
		if err != nil {
			return total, err
		}
		n := int(tag.RowsAffected())
		total += n
		if n < retentionBatch {
			break
		}
	}
	return total, ctx.Err()
}

// RetentionOnce membersihkan data sesuai pengaturan. Aman diulang (idempoten) dan dikerjakan per batch agar tidak mengunci tabel lama.
func (s *Service) RetentionOnce(ctx context.Context) (RetentionRun, error) {
	cfg, err := s.GetRetention(ctx)
	run := RetentionRun{At: time.Now().UTC()}
	if err != nil {
		return run, err
	}
	// 1. isi pesan pada pesan yang sudah selesai (metadata dipertahankan; pesan aktif tidak disentuh)
	if run.BodiesPurged, err = s.batchExec(ctx, `UPDATE messages SET body=NULL WHERE id IN (
		SELECT id FROM messages WHERE body IS NOT NULL AND status NOT IN ('queued','assigned','sending')
		  AND COALESCE(finished_at, delivered_at, sent_at, created_at) < now() - ($1::int * interval '1 day') LIMIT $2)`, cfg.BodyRetentionDays, retentionBatch); err != nil {
		return run, err
	}
	// 2. data pribadi pada penerima campaign yang sudah selesai: variabel, nomor mentah, client_ref (nomor E.164 tetap, sama seperti pada pesan)
	if run.RecipientsScrubbed, err = s.batchExec(ctx, `UPDATE campaign_recipients SET vars='{}'::jsonb, to_raw=to_e164, client_ref=NULL WHERE id IN (
		SELECT r.id FROM campaign_recipients r JOIN campaigns c ON c.id=r.campaign_id
		WHERE c.status IN ('completed','cancelled') AND c.finished_at < now() - ($1::int * interval '1 day')
		  AND (r.vars <> '{}'::jsonb OR r.to_raw <> r.to_e164 OR r.client_ref IS NOT NULL) LIMIT $2)`, cfg.BodyRetentionDays, retentionBatch); err != nil {
		return run, err
	}
	// 3. timeline event
	if run.EventsDeleted, err = s.batchExec(ctx, `DELETE FROM message_events WHERE id IN (SELECT id FROM message_events WHERE at < now() - ($1::int * interval '1 day') LIMIT $2)`, cfg.EventRetentionDays, retentionBatch); err != nil {
		return run, err
	}
	// 4. housekeeping tetap: sesi, enrollment, riwayat webhook (audit_logs TIDAK dihapus)
	if run.SessionsDeleted, err = s.batchExec(ctx, `DELETE FROM sessions WHERE id IN (SELECT id FROM sessions WHERE expires_at < now() OR last_seen_at < now() - interval '12 hours' LIMIT $1)`, retentionBatch); err != nil {
		return run, err
	}
	if run.EnrollmentsDeleted, err = s.batchExec(ctx, `DELETE FROM device_enrollments WHERE id IN (SELECT id FROM device_enrollments
		WHERE (consumed_at IS NOT NULL OR revoked_at IS NOT NULL OR expires_at < now()) AND created_at < now() - interval '30 days' LIMIT $1)`, retentionBatch); err != nil {
		return run, err
	}
	if run.DeliveriesDeleted, err = s.batchExec(ctx, `DELETE FROM webhook_deliveries WHERE id IN (SELECT id FROM webhook_deliveries
		WHERE COALESCE(delivered_at, failed_at) < now() - interval '30 days' LIMIT $1)`, retentionBatch); err != nil {
		return run, err
	}
	// 5. undangan yang sudah selesai (diterima / dicabut / kedaluwarsa) lebih lama dari masa simpan; yang masih tertunda tidak disentuh
	if run.InvitationsDeleted, err = s.batchExec(ctx, `DELETE FROM invitations WHERE id IN (SELECT id FROM invitations
		WHERE COALESCE(accepted_at, revoked_at, expires_at) < now() - ($1::int * interval '1 day')
		  AND (accepted_at IS NOT NULL OR revoked_at IS NOT NULL OR expires_at < now()) LIMIT $2)`, cfg.InvitationRetentionDays, retentionBatch); err != nil {
		return run, err
	}
	s.M.RetentionRuns.Inc()
	for kind, n := range map[string]int{"message_bodies": run.BodiesPurged, "recipients": run.RecipientsScrubbed, "message_events": run.EventsDeleted,
		"sessions": run.SessionsDeleted, "enrollments": run.EnrollmentsDeleted, "webhook_deliveries": run.DeliveriesDeleted, "invitations": run.InvitationsDeleted} {
		s.M.RetentionRows.WithLabelValues(kind).Add(float64(n))
	}
	if raw, e := json.Marshal(run); e == nil {
		_, _ = s.Pool.Exec(ctx, `INSERT INTO settings(key,value) VALUES ('retention_last_run',$1) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, raw)
	}
	if run.BodiesPurged+run.EventsDeleted+run.RecipientsScrubbed > 0 { // jejak audit hanya berisi jumlah, bukan isi
		s.Audit(ctx, "system", "retention.run", "settings", "retention", map[string]any{"bodies_purged": run.BodiesPurged, "events_deleted": run.EventsDeleted, "recipients_scrubbed": run.RecipientsScrubbed})
	}
	return run, nil
}

// ---------- penghapusan data pribadi per nomor (UU PDP: hak hapus) ----------

type EraseResult struct {
	Messages   int  `json:"messages"`
	Recipients int  `json:"recipients"`
	Deliveries int  `json:"webhook_deliveries"`
	Suppressed bool `json:"suppressed"`
}

// EraseNumber menganonimkan semua jejak sebuah nomor: pesan (nomor+isi+client_ref), penerima campaign, payload webhook.
// Ditolak bila masih ada pesan yang belum selesai (menganonimkan pesan aktif akan merusak pengiriman).
func (s *Service) EraseNumber(ctx context.Context, number string, suppress bool, actor string) (EraseResult, error) {
	var res EraseResult
	e164, err := sms.Normalize(number, s.Cfg.DefaultRegion)
	if err != nil {
		return res, Validation(FieldError{Field: "number", Message: "nomor tidak valid"})
	}
	var active int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE to_e164=$1 AND status IN ('queued','assigned','sending')`, e164).Scan(&active); err != nil {
		return res, err
	}
	if active > 0 {
		return res, problem(http.StatusConflict, "privacy.active_messages", "Masih ada pesan aktif", fmt.Sprintf("%d pesan ke nomor ini belum selesai; batalkan atau tunggu selesai lebih dulu", active))
	}
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE messages SET to_e164='erased:'||id::text, body=NULL, client_ref=NULL WHERE to_e164=$1`, e164)
		if err != nil {
			return err
		}
		res.Messages = int(tag.RowsAffected())
		tag, err = tx.Exec(ctx, `DELETE FROM campaign_recipients WHERE to_e164=$1`, e164)
		if err != nil {
			return err
		}
		res.Recipients = int(tag.RowsAffected())
		tag, err = tx.Exec(ctx, `UPDATE webhook_deliveries SET payload = jsonb_set(payload, '{to}', '"erased"') WHERE payload->>'to' = $1`, e164)
		if err != nil {
			return err
		}
		res.Deliveries = int(tag.RowsAffected())
		if suppress {
			if _, err := tx.Exec(ctx, `INSERT INTO suppressions(id,number_e164,reason) VALUES ($1,$2,'permintaan penghapusan data') ON CONFLICT (number_e164) DO NOTHING`, newID(), e164); err != nil {
				return err
			}
			res.Suppressed = true
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	h := sha256.Sum256([]byte(e164))
	s.Audit(ctx, actor, "privacy.erase", "number", hex.EncodeToString(h[:6]), map[string]any{"messages": res.Messages, "recipients": res.Recipients, "suppressed": res.Suppressed}) // hanya hash, bukan nomor
	return res, nil
}
