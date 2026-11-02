package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jipaw/smsc-app/server/internal/sms"
)

// ---------- template ----------

type Template struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Body      string    `json:"body"`
	Variables []string  `json:"variables"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Service) SaveTemplate(ctx context.Context, id *uuid.UUID, name, body string) (Template, error) {
	var errs []FieldError
	if name == "" {
		errs = append(errs, FieldError{"name", "wajib diisi"})
	}
	if body == "" || len([]rune(body)) > 1600 {
		errs = append(errs, FieldError{"body", "wajib diisi, maksimal 1600 karakter"})
	}
	if len(errs) > 0 {
		return Template{}, Validation(errs...)
	}
	vars := sms.Variables(body)
	var t Template
	var err error
	if id == nil {
		err = s.Pool.QueryRow(ctx, `INSERT INTO templates(id,name,body,variables) VALUES ($1,$2,$3,$4) RETURNING id,name,body,variables,updated_at`,
			newID(), name, body, vars).Scan(&t.ID, &t.Name, &t.Body, &t.Variables, &t.UpdatedAt)
	} else {
		err = s.Pool.QueryRow(ctx, `UPDATE templates SET name=$2,body=$3,variables=$4,updated_at=now() WHERE id=$1 RETURNING id,name,body,variables,updated_at`,
			*id, name, body, vars).Scan(&t.ID, &t.Name, &t.Body, &t.Variables, &t.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return t, NotFound("template")
		}
	}
	if isUnique(err, "") {
		return t, NewProblem(http.StatusConflict, "resource.conflict", "Konflik", "nama template sudah dipakai")
	}
	return t, err
}

func (s *Service) ListTemplates(ctx context.Context) ([]Template, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,name,body,variables,updated_at FROM templates ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Template, error) {
		var t Template
		return t, r.Scan(&t.ID, &t.Name, &t.Body, &t.Variables, &t.UpdatedAt)
	})
}

func (s *Service) GetTemplate(ctx context.Context, id uuid.UUID) (Template, error) {
	var t Template
	err := s.Pool.QueryRow(ctx, `SELECT id,name,body,variables,updated_at FROM templates WHERE id=$1`, id).Scan(&t.ID, &t.Name, &t.Body, &t.Variables, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, NotFound("template")
	}
	return t, err
}

func (s *Service) DeleteTemplate(ctx context.Context, id uuid.UUID) error {
	ct, err := s.Pool.Exec(ctx, `DELETE FROM templates WHERE id=$1`, id)
	if err == nil && ct.RowsAffected() == 0 {
		return NotFound("template")
	}
	return err
}

// ---------- suppression ----------

type Suppression struct {
	ID        uuid.UUID `json:"id"`
	Number    string    `json:"number"`
	Reason    *string   `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Service) AddSuppressions(ctx context.Context, by uuid.UUID, numbers []string, reason string) (added int, invalid []string, err error) {
	for _, n := range numbers {
		e164, nerr := sms.Normalize(n, s.Cfg.DefaultRegion)
		if nerr != nil {
			invalid = append(invalid, n)
			continue
		}
		ct, e := s.Pool.Exec(ctx, `INSERT INTO suppressions(id,number_e164,reason,created_by) VALUES ($1,$2,$3,$4) ON CONFLICT (number_e164) DO NOTHING`, newID(), e164, nullStr(reason), by)
		if e != nil {
			return added, invalid, e
		}
		added += int(ct.RowsAffected())
	}
	return
}

func (s *Service) ListSuppressions(ctx context.Context, limit int, cursor string) ([]Suppression, string, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	args := []any{limit + 1}
	q := `SELECT id,number_e164,reason,created_at FROM suppressions`
	if cursor != "" {
		t, id, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", Validation(FieldError{"cursor", "tidak valid"})
		}
		args = append(args, t, id)
		q += ` WHERE (created_at, id) < ($2, $3)`
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT $1`
	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Suppression, error) {
		var x Suppression
		return x, r.Scan(&x.ID, &x.Number, &x.Reason, &x.CreatedAt)
	})
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = encodeCursor(out[len(out)-1].CreatedAt, out[len(out)-1].ID)
	}
	if out == nil {
		out = []Suppression{}
	}
	return out, next, err
}

func (s *Service) DeleteSuppression(ctx context.Context, id uuid.UUID) error {
	ct, err := s.Pool.Exec(ctx, `DELETE FROM suppressions WHERE id=$1`, id)
	if err == nil && ct.RowsAffected() == 0 {
		return NotFound("entri daftar blokir")
	}
	return err
}

// ---------- audit ----------

type AuditLog struct {
	ID         uuid.UUID      `json:"id"`
	At         time.Time      `json:"at"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	TargetType *string        `json:"target_type"`
	TargetID   *string        `json:"target_id"`
	Data       map[string]any `json:"data"`
}

func (s *Service) ListAudit(ctx context.Context, limit int, cursor string) ([]AuditLog, string, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	args := []any{limit + 1}
	q := `SELECT id,at,actor,action,target_type,target_id,data FROM audit_logs`
	if cursor != "" {
		t, id, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", Validation(FieldError{"cursor", "tidak valid"})
		}
		args = append(args, t, id)
		q += ` WHERE (at, id) < ($2, $3)`
	}
	q += ` ORDER BY at DESC, id DESC LIMIT $1`
	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AuditLog, error) {
		var x AuditLog
		return x, r.Scan(&x.ID, &x.At, &x.Actor, &x.Action, &x.TargetType, &x.TargetID, &x.Data)
	})
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = encodeCursor(out[len(out)-1].At, out[len(out)-1].ID)
	}
	if out == nil {
		out = []AuditLog{}
	}
	return out, next, err
}

// ---------- statistik ----------

func (s *Service) Overview(ctx context.Context) (map[string]any, error) {
	dev := map[string]int{"online": 0, "degraded": 0, "offline": 0}
	rows, err := s.Pool.Query(ctx, `SELECT status::text, count(*) FROM devices WHERE status IN ('online','degraded','offline') GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n int
		_ = rows.Scan(&st, &n)
		dev[st] = n
	}
	rows.Close()
	var simsActive, queue, acc, sent, deliv, failed, ok24, bad24 int
	err = s.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM sims s JOIN devices d ON d.id=s.device_id WHERE s.enabled AND s.status='active' AND d.status IN ('online','degraded')),
		(SELECT count(*) FROM messages WHERE status='queued'),
		(SELECT count(*) FROM messages WHERE created_at >= date_trunc('day', now())),
		(SELECT count(*) FROM messages WHERE sent_at >= date_trunc('day', now())),
		(SELECT count(*) FROM messages WHERE delivered_at >= date_trunc('day', now())),
		(SELECT count(*) FROM messages WHERE status='failed' AND finished_at >= date_trunc('day', now())),
		(SELECT count(*) FROM messages WHERE sent_at >= now() - interval '24 hours'),
		(SELECT count(*) FROM messages WHERE status='failed' AND finished_at >= now() - interval '24 hours')`).
		Scan(&simsActive, &queue, &acc, &sent, &deliv, &failed, &ok24, &bad24)
	if err != nil {
		return nil, err
	}
	rate := 0.0
	if ok24+bad24 > 0 {
		rate = float64(ok24) / float64(ok24+bad24)
	}
	return map[string]any{
		"devices": dev, "sims_active": simsActive, "queue_depth": queue,
		"today":            map[string]int{"accepted": acc, "sent": sent, "delivered": deliv, "failed": failed},
		"success_rate_24h": rate, "paused": s.Paused(ctx),
	}, nil
}

type Point struct {
	T         time.Time `json:"t"`
	Sent      int       `json:"sent"`
	Delivered int       `json:"delivered"`
	Failed    int       `json:"failed"`
}

func (s *Service) Timeseries(ctx context.Context, from, to time.Time, bucket string, campaignID, simID *uuid.UUID) ([]Point, error) {
	switch bucket {
	case "minute", "hour", "day":
	case "":
		bucket = "hour"
	default:
		return nil, Validation(FieldError{"bucket", "minute|hour|day"})
	}
	q := fmt.Sprintf(`SELECT date_trunc('%s', COALESCE(delivered_at, sent_at, finished_at)) AS t,
		count(*) FILTER (WHERE sent_at IS NOT NULL), count(*) FILTER (WHERE delivered_at IS NOT NULL), count(*) FILTER (WHERE status='failed')
		FROM messages WHERE COALESCE(delivered_at, sent_at, finished_at) >= $1 AND COALESCE(delivered_at, sent_at, finished_at) < $2
		  AND ($3::uuid IS NULL OR campaign_id=$3) AND ($4::uuid IS NULL OR sim_id=$4) GROUP BY 1 ORDER BY 1`, bucket)
	rows, err := s.Pool.Query(ctx, q, from, to, campaignID, simID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Point, error) {
		var p Point
		return p, r.Scan(&p.T, &p.Sent, &p.Delivered, &p.Failed)
	})
	if out == nil {
		out = []Point{}
	}
	return out, err
}
