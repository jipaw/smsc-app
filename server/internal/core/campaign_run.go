package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jipaw/smsc-app/server/internal/policy"
	"github.com/jipaw/smsc-app/server/internal/sms"
)

// ---------- kontrol status ----------

func (s *Service) transitionFail(ctx context.Context, id uuid.UUID, action string) error {
	var st string
	if err := s.Pool.QueryRow(ctx, `SELECT status::text FROM campaigns WHERE id=$1`, id).Scan(&st); errors.Is(err, pgx.ErrNoRows) {
		return NotFound("campaign")
	} else if err != nil {
		return err
	}
	return badTransition(fmt.Sprintf("campaign berstatus %q tidak dapat di-%s", st, action))
}

func (s *Service) StartCampaign(ctx context.Context, id uuid.UUID, actor string) (Campaign, error) {
	c, err := scanCampaign(s.Pool.QueryRow(ctx, `SELECT `+campCols+` FROM campaigns WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, NotFound("campaign")
	}
	if err != nil {
		return Campaign{}, err
	}
	if c.Status != "draft" {
		return Campaign{}, badTransition(fmt.Sprintf("campaign berstatus %q tidak dapat di-start", c.Status))
	}
	var errs []FieldError
	var n int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id=$1`, id).Scan(&n); err != nil {
		return Campaign{}, err
	}
	if n == 0 {
		errs = append(errs, FieldError{Field: "recipients", Message: "belum ada penerima; impor penerima lebih dulu"})
	}
	text, err := s.campaignText(ctx, c)
	if err != nil {
		return Campaign{}, err
	}
	if text == "" {
		errs = append(errs, FieldError{Field: "body", Message: "isi pesan atau template belum diisi"})
	}
	miss, err := s.missingVariables(ctx, id, sms.Variables(text))
	if err != nil {
		return Campaign{}, err
	}
	for _, m := range miss {
		errs = append(errs, FieldError{Field: "recipients", Message: fmt.Sprintf("%d penerima tidak memiliki variabel {{%s}}", m.Recipients, m.Name)})
	}
	if len(errs) > 0 {
		return Campaign{}, Validation(errs...)
	}
	if _, err := s.Effective(ctx, s.Pool, nil, nil, c.PolicyID); errors.Is(err, policy.ErrEmptyWindow) {
		return Campaign{}, problem(http.StatusUnprocessableEntity, "policy.empty_window", "Jendela waktu kosong", "irisan jendela waktu policy tidak pernah terbuka")
	} else if err != nil {
		return Campaign{}, err
	}
	if s.eligibleSims(ctx, c.SimIDs, false) == 0 {
		return Campaign{}, problem(http.StatusUnprocessableEntity, "campaign.no_capacity", "Tidak ada SIM", "tidak ada SIM aktif yang sesuai sim_ids; daftarkan device atau ubah pilihan SIM")
	}
	next := "running"
	if c.Schedule.StartAt != nil && c.Schedule.StartAt.After(time.Now().Add(time.Second)) {
		next = "scheduled"
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE campaigns SET status=$2::campaign_status, started_at=CASE WHEN $2='running' THEN now() END, eval_since=now(), pause_reason=NULL WHERE id=$1 AND status='draft'`, id, next)
	if err != nil {
		return Campaign{}, err
	}
	if tag.RowsAffected() == 0 {
		return Campaign{}, s.transitionFail(ctx, id, "start")
	}
	s.Audit(ctx, actor, "campaign."+map[string]string{"running": "start", "scheduled": "schedule"}[next], "campaign", id.String(), map[string]any{"recipients": n})
	s.publishCampaign(ctx, id)
	s.Wake()
	return s.GetCampaign(ctx, id)
}

func (s *Service) PauseCampaign(ctx context.Context, id uuid.UUID, actor string) (Campaign, error) {
	return s.simpleTransition(ctx, id, actor, "pause", `UPDATE campaigns SET status='paused', pause_reason='user' WHERE id=$1 AND status='running'`)
}

func (s *Service) ResumeCampaign(ctx context.Context, id uuid.UUID, actor string) (Campaign, error) {
	s.capSince.Delete(id)
	return s.simpleTransition(ctx, id, actor, "resume", `UPDATE campaigns SET status='running', pause_reason=NULL, eval_since=now() WHERE id=$1 AND status='paused'`)
}

func (s *Service) simpleTransition(ctx context.Context, id uuid.UUID, actor, action, q string) (Campaign, error) {
	tag, err := s.Pool.Exec(ctx, q, id)
	if err != nil {
		return Campaign{}, err
	}
	if tag.RowsAffected() == 0 {
		return Campaign{}, s.transitionFail(ctx, id, action)
	}
	s.Audit(ctx, actor, "campaign."+action, "campaign", id.String(), nil)
	s.publishCampaign(ctx, id)
	s.Wake()
	return s.GetCampaign(ctx, id)
}

// CancelCampaign: pesan queued -> cancelled (jalur bulk yang sah menurut graf: queued->cancelled, event tetap ditulis);
// yang sudah di tangan HP (assigned/sending) diselesaikan.
func (s *Service) CancelCampaign(ctx context.Context, id uuid.UUID, actor string) (Campaign, error) {
	var cancelled int64
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE campaigns SET status='cancelled', finished_at=now(), pause_reason=NULL WHERE id=$1 AND status IN ('scheduled','running','paused')`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNoRows
		}
		tag, err = tx.Exec(ctx, `WITH c AS (
				UPDATE messages SET status='cancelled', finished_at=now(), lease_expires_at=NULL WHERE campaign_id=$1 AND status='queued' RETURNING id, attempt)
			INSERT INTO message_events(id,message_id,attempt,type,actor,data)
			SELECT gen_random_uuid(), id, attempt, 'cancelled', 'system', '{"reason":"campaign_cancelled"}'::jsonb FROM c`, id)
		cancelled = tag.RowsAffected()
		return err
	})
	if errors.Is(err, errNoRows) {
		return Campaign{}, s.transitionFail(ctx, id, "cancel")
	}
	if err != nil {
		return Campaign{}, err
	}
	s.capSince.Delete(id)
	s.Audit(ctx, actor, "campaign.cancel", "campaign", id.String(), map[string]any{"messages_cancelled": cancelled})
	s.publishCampaign(ctx, id)
	s.Wake()
	return s.GetCampaign(ctx, id)
}

var errNoRows = errors.New("tidak ada baris")

// ---------- loop latar belakang (leader) ----------

func (s *Service) RunCampaigns(ctx context.Context) {
	t := time.NewTicker(s.Cfg.CampaignTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.campaignTick(ctx)
		}
	}
}

func (s *Service) campaignTick(ctx context.Context) {
	// scheduled -> running
	rows, err := s.Pool.Query(ctx, `UPDATE campaigns SET status='running', started_at=COALESCE(started_at, now()), eval_since=now()
		WHERE status='scheduled' AND start_at <= now() RETURNING id`)
	if err == nil {
		var started []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if rows.Scan(&id) == nil {
				started = append(started, id)
			}
		}
		rows.Close()
		for _, id := range started {
			s.Log.Info("campaign terjadwal dimulai", "campaign", id)
			s.Audit(ctx, "system", "campaign.start", "campaign", id.String(), map[string]any{"scheduled": true})
			s.publishCampaign(ctx, id)
		}
	}
	for _, id := range s.idsQuery(ctx, `SELECT id FROM campaigns WHERE status='running'`) {
		s.runCampaign(ctx, id)
	}
}

func (s *Service) runCampaign(ctx context.Context, id uuid.UUID) {
	c, err := scanCampaign(s.Pool.QueryRow(ctx, `SELECT `+campCols+` FROM campaigns WHERE id=$1 AND status='running'`, id))
	if err != nil {
		return
	}
	if n, err := s.materialize(ctx, c); err != nil {
		s.Log.Error("campaign: materialisasi", "err", err, "campaign", id)
	} else if n > 0 {
		s.publishCampaign(ctx, id)
		s.Wake()
	}
	if s.autoPauseCheck(ctx, c) {
		return
	}
	if s.capacityCheck(ctx, c) {
		return
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE campaigns SET status='completed', finished_at=now() WHERE id=$1 AND status='running'
		AND NOT EXISTS (SELECT 1 FROM campaign_recipients WHERE campaign_id=$1 AND materialized_at IS NULL)
		AND NOT EXISTS (SELECT 1 FROM messages WHERE campaign_id=$1 AND status IN ('queued','assigned','sending'))`, id)
	if err == nil && tag.RowsAffected() == 1 {
		s.Log.Info("campaign selesai", "campaign", id)
		s.Audit(ctx, "system", "campaign.complete", "campaign", id.String(), nil)
		s.publishCampaign(ctx, id)
	}
}

// materialize: ubah penerima menjadi pesan (batch <= 1.000; antrean campaign dibatasi CampaignQueueCap).
func (s *Service) materialize(ctx context.Context, c Campaign) (int, error) {
	var queued int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE campaign_id=$1 AND status='queued'`, c.ID).Scan(&queued); err != nil {
		return 0, err
	}
	room := s.Cfg.CampaignQueueCap - queued
	if room <= 0 {
		return 0, nil
	}
	if room > 1000 {
		room = 1000
	}
	text, err := s.campaignText(ctx, c)
	if err != nil {
		return 0, err
	}
	eff, err := s.Effective(ctx, s.Pool, nil, nil, c.PolicyID)
	if err != nil {
		return 0, err
	}
	n := 0
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, to_e164, vars FROM campaign_recipients WHERE campaign_id=$1 AND materialized_at IS NULL ORDER BY row_no LIMIT $2 FOR UPDATE SKIP LOCKED`, c.ID, room)
		if err != nil {
			return err
		}
		type rec struct {
			id   uuid.UUID
			to   string
			vars map[string]string
		}
		var recs []rec
		var tos []string
		for rows.Next() {
			var r rec
			if err := rows.Scan(&r.id, &r.to, &r.vars); err != nil {
				rows.Close()
				return err
			}
			recs = append(recs, r)
			tos = append(tos, r.to)
		}
		rows.Close()
		if len(recs) == 0 {
			return nil
		}
		supp := map[string]bool{} // opt-out yang masuk setelah impor tetap dihormati
		srows, err := tx.Query(ctx, `SELECT number_e164 FROM suppressions WHERE number_e164 = ANY($1)`, tos)
		if err != nil {
			return err
		}
		for srows.Next() {
			var x string
			_ = srows.Scan(&x)
			supp[x] = true
		}
		srows.Close()
		expires := time.Now().Add(time.Duration(eff.MessageTTLSeconds) * time.Second)
		b := &pgx.Batch{}
		ids := make([]uuid.UUID, 0, len(recs))
		for _, r := range recs {
			mid := newID()
			body, missing := sms.Render(text, r.vars)
			status, code := "queued", (*string)(nil)
			var finished *time.Time
			if supp[r.to] {
				status, code, finished = "failed", ptr("suppressed"), ptr(time.Now())
			} else if len(missing) > 0 {
				status, code, finished = "failed", ptr("missing_variable"), ptr(time.Now())
			}
			info := sms.Analyze(body)
			b.Queue(`INSERT INTO messages(id,source,campaign_id,recipient_id,to_e164,body,encoding,parts,status,error_code,max_attempts,priority,policy_id,not_before,expires_at,finished_at)
				VALUES ($1,'campaign',$2,$3,$4,$5,$6,$7,$8::message_status,$9,$10,$11,$12,now(),$13,$14)`,
				mid, c.ID, r.id, r.to, body, info.Encoding, info.Parts, status, code, eff.MaxAttempts, c.Priority, c.PolicyID, expires, finished)
			b.Queue(`INSERT INTO message_events(id,message_id,attempt,type,actor,data) VALUES ($1,$2,0,'created','system','{"source":"campaign"}')`, newID(), mid)
			second := "queued"
			if status == "failed" {
				second = "failed"
			}
			b.Queue(`INSERT INTO message_events(id,message_id,attempt,type,actor,error_code,data) VALUES ($1,$2,0,$3,'system',$4,'{}')`, newID(), mid, second, code)
			ids = append(ids, r.id)
		}
		br := tx.SendBatch(ctx, b)
		for i := 0; i < b.Len(); i++ {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return err
			}
		}
		if err := br.Close(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE campaign_recipients SET materialized_at=now() WHERE id = ANY($1)`, ids); err != nil {
			return err
		}
		n = len(ids)
		return nil
	})
	return n, err
}

// kegagalan "infrastruktur" (SIM/jaringan/HP); invalid_destination dll. tidak mencerminkan kesehatan SIM.
var infraCodes = []string{"no_service", "radio_off", "generic_failure", "os_rate_limited", "ack_timeout", "sim_unavailable", "device_lost", "device_revoked"}

// autoPauseCheck: rasio gagal per PERCOBAAN pada `window` percobaan terakhir sejak baseline (start/resume). Butuh >= min(window,10) sampel.
func (s *Service) autoPauseCheck(ctx context.Context, c Campaign) bool {
	ap := c.AutoPause
	if !ap.Enabled {
		return false
	}
	since := time.Now().Add(-time.Hour)
	if c.evalSince != nil && c.evalSince.After(since) {
		since = *c.evalSince
	}
	rows, err := s.Pool.Query(ctx, `SELECT (e.type='sent') FROM message_events e JOIN messages m ON m.id=e.message_id
		WHERE m.campaign_id=$1 AND e.at > $2 AND (e.type='sent' OR (e.type IN ('requeued','failed') AND e.error_code = ANY($3)))
		ORDER BY e.at DESC LIMIT $4`, c.ID, since, infraCodes, ap.Window)
	if err != nil {
		return false
	}
	defer rows.Close()
	total, fails := 0, 0
	for rows.Next() {
		var ok bool
		if rows.Scan(&ok) == nil {
			total++
			if !ok {
				fails++
			}
		}
	}
	need := ap.Window
	if need > 10 {
		need = 10
	}
	if total < need || float64(fails)/float64(total) <= ap.MaxFailureRatio {
		return false
	}
	return s.autoPause(ctx, c.ID, "auto_failure_ratio", map[string]any{"failures": fails, "sample": total, "ratio": float64(fails) / float64(total)})
}

func (s *Service) autoPause(ctx context.Context, id uuid.UUID, reason string, data map[string]any) bool {
	tag, err := s.Pool.Exec(ctx, `UPDATE campaigns SET status='paused', pause_reason=$2 WHERE id=$1 AND status='running'`, id, reason)
	if err != nil || tag.RowsAffected() == 0 {
		return false
	}
	s.Log.Warn("campaign dijeda otomatis", "campaign", id, "reason", reason)
	s.Audit(ctx, "system", "campaign.auto_pause", "campaign", id.String(), mergeMap(data, map[string]any{"reason": reason}))
	s.publishCampaign(ctx, id)
	return true
}

func mergeMap(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// capacityCheck: ada pekerjaan tersisa tetapi tidak ada SIM online selama NoCapacityAfter -> pause(no_capacity).
func (s *Service) capacityCheck(ctx context.Context, c Campaign) bool {
	var work bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE campaign_id=$1 AND status='queued') OR EXISTS(SELECT 1 FROM campaign_recipients WHERE campaign_id=$1 AND materialized_at IS NULL)`, c.ID).Scan(&work); err != nil {
		return false
	}
	if !work || s.eligibleSims(ctx, c.SimIDs, true) > 0 {
		s.capSince.Delete(c.ID)
		return false
	}
	v, _ := s.capSince.LoadOrStore(c.ID, time.Now())
	if time.Since(v.(time.Time)) < s.Cfg.NoCapacityAfter {
		return false
	}
	s.capSince.Delete(c.ID)
	return s.autoPause(ctx, c.ID, "no_capacity", nil)
}
