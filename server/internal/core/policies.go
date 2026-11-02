package core

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jipaw/smsc-app/server/internal/policy"
)

// PolicyDTO mengikuti DispatchPolicy/DispatchPolicyInput di OpenAPI (rate_limits bersarang).
type PolicyDTO struct {
	ID                *uuid.UUID `json:"id,omitempty"`
	Name              string     `json:"name"`
	IsDefault         *bool      `json:"is_default,omitempty"`
	UpdatedAt         *time.Time `json:"updated_at,omitempty"`
	MinDelayMs        *int       `json:"min_delay_ms"`
	MaxDelayMs        *int       `json:"max_delay_ms"`
	BurstSize         *int       `json:"burst_size"`
	BurstPauseMs      *int       `json:"burst_pause_ms"`
	MaxInflightPerSim *int       `json:"max_inflight_per_sim"`
	RateLimits        *struct {
		PerMinute *int `json:"per_minute"`
		PerHour   *int `json:"per_hour"`
		PerDay    *int `json:"per_day"`
	} `json:"rate_limits,omitempty"`
	SendWindow        *policy.Window `json:"send_window"`
	MaxAttempts       *int           `json:"max_attempts"`
	RetryBackoffMs    *int           `json:"retry_backoff_ms"`
	RetryBackoffMaxMs *int           `json:"retry_backoff_max_ms"`
	MessageTTLSeconds *int           `json:"message_ttl_seconds"`
	TrackDelivery     *bool          `json:"track_delivery"`
	AckTimeoutMs      *int           `json:"ack_timeout_ms"`
	SentTimeoutMs     *int           `json:"sent_timeout_ms"`
	RetryOnUnknown    *bool          `json:"retry_on_unknown"`
}

func (d PolicyDTO) toPolicy() policy.Policy {
	p := policy.Policy{
		MinDelayMs: d.MinDelayMs, MaxDelayMs: d.MaxDelayMs, BurstSize: d.BurstSize, BurstPauseMs: d.BurstPauseMs,
		MaxInflightPerSim: d.MaxInflightPerSim, SendWindow: d.SendWindow, MaxAttempts: d.MaxAttempts,
		RetryBackoffMs: d.RetryBackoffMs, RetryBackoffMaxMs: d.RetryBackoffMaxMs, MessageTTLSeconds: d.MessageTTLSeconds,
		TrackDelivery: d.TrackDelivery, AckTimeoutMs: d.AckTimeoutMs, SentTimeoutMs: d.SentTimeoutMs, RetryOnUnknown: d.RetryOnUnknown,
	}
	if d.RateLimits != nil {
		p.RatePerMinute, p.RatePerHour, p.RatePerDay = d.RateLimits.PerMinute, d.RateLimits.PerHour, d.RateLimits.PerDay
	}
	return p
}

const policyCols = `id,name,is_default,updated_at,min_delay_ms,max_delay_ms,burst_size,burst_pause_ms,max_inflight_per_sim,
	rate_per_minute,rate_per_hour,rate_per_day,send_window,max_attempts,retry_backoff_ms,retry_backoff_max_ms,message_ttl_seconds,
	track_delivery,ack_timeout_ms,sent_timeout_ms,retry_on_unknown`

func scanPolicy(r pgx.Row) (PolicyDTO, error) {
	var d PolicyDTO
	var rpm, rph, rpd *int
	var id uuid.UUID
	var def bool
	var up time.Time
	err := r.Scan(&id, &d.Name, &def, &up, &d.MinDelayMs, &d.MaxDelayMs, &d.BurstSize, &d.BurstPauseMs, &d.MaxInflightPerSim,
		&rpm, &rph, &rpd, &d.SendWindow, &d.MaxAttempts, &d.RetryBackoffMs, &d.RetryBackoffMaxMs, &d.MessageTTLSeconds,
		&d.TrackDelivery, &d.AckTimeoutMs, &d.SentTimeoutMs, &d.RetryOnUnknown)
	d.ID, d.IsDefault, d.UpdatedAt = &id, &def, &up
	d.RateLimits = &struct {
		PerMinute *int `json:"per_minute"`
		PerHour   *int `json:"per_hour"`
		PerDay    *int `json:"per_day"`
	}{rpm, rph, rpd}
	return d, err
}

type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadPolicy(ctx context.Context, q queryer, id *uuid.UUID) (policy.Policy, error) {
	if id == nil {
		return policy.Policy{}, nil
	}
	d, err := scanPolicy(q.QueryRow(ctx, `SELECT `+policyCols+` FROM dispatch_policies WHERE id=$1`, *id))
	if errors.Is(err, pgx.ErrNoRows) {
		return policy.Policy{}, nil
	}
	return d.toPolicy(), err
}

func (s *Service) globalPolicy(ctx context.Context, q queryer) (policy.Policy, error) {
	d, err := scanPolicy(q.QueryRow(ctx, `SELECT `+policyCols+` FROM dispatch_policies WHERE is_default`))
	return d.toPolicy(), err
}

// Effective menyelesaikan policy untuk kombinasi device/SIM (+ policy pesan sebagai preferensi).
func (s *Service) Effective(ctx context.Context, q queryer, devPolicy, simPolicy, msgPolicy *uuid.UUID) (policy.Effective, error) {
	g, err := s.globalPolicy(ctx, q)
	if err != nil {
		return policy.Effective{}, err
	}
	dp, _ := loadPolicy(ctx, q, devPolicy)
	sp, _ := loadPolicy(ctx, q, simPolicy)
	mp, _ := loadPolicy(ctx, q, msgPolicy)
	return policy.Resolve(g, []policy.Policy{dp, sp}, []policy.Policy{mp})
}

func (s *Service) ListPolicies(ctx context.Context) ([]PolicyDTO, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+policyCols+` FROM dispatch_policies ORDER BY is_default DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PolicyDTO{}
	for rows.Next() {
		d, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Service) GetPolicy(ctx context.Context, id uuid.UUID) (PolicyDTO, error) {
	d, err := scanPolicy(s.Pool.QueryRow(ctx, `SELECT `+policyCols+` FROM dispatch_policies WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, NotFound("policy")
	}
	return d, err
}

func (d PolicyDTO) validate() error {
	var errs []FieldError
	if d.Name == "" {
		errs = append(errs, FieldError{"name", "wajib diisi"})
	}
	if d.MinDelayMs != nil && d.MaxDelayMs != nil && *d.MaxDelayMs < *d.MinDelayMs {
		errs = append(errs, FieldError{"max_delay_ms", "harus >= min_delay_ms"})
	}
	if d.SendWindow != nil {
		if err := d.SendWindow.Validate(); err != nil {
			errs = append(errs, FieldError{"send_window", err.Error()})
		} else if len(d.SendWindow.Windows) > 0 && !policy.EverOpen([]policy.Window{*d.SendWindow}, time.Now()) {
			errs = append(errs, FieldError{"send_window", "jendela tidak pernah terbuka"})
		}
	}
	if len(errs) > 0 {
		return Validation(errs...)
	}
	return nil
}

func (s *Service) SavePolicy(ctx context.Context, id *uuid.UUID, d PolicyDTO) (PolicyDTO, error) {
	if err := d.validate(); err != nil {
		return d, err
	}
	p := d.toPolicy()
	args := []any{d.Name, p.MinDelayMs, p.MaxDelayMs, p.BurstSize, p.BurstPauseMs, p.MaxInflightPerSim, p.RatePerMinute, p.RatePerHour, p.RatePerDay,
		p.SendWindow, p.MaxAttempts, p.RetryBackoffMs, p.RetryBackoffMaxMs, p.MessageTTLSeconds, p.TrackDelivery, p.AckTimeoutMs, p.SentTimeoutMs, p.RetryOnUnknown}
	var out PolicyDTO
	var err error
	if id == nil {
		nid := newID()
		out, err = scanPolicy(s.Pool.QueryRow(ctx, `INSERT INTO dispatch_policies(name,min_delay_ms,max_delay_ms,burst_size,burst_pause_ms,max_inflight_per_sim,
			rate_per_minute,rate_per_hour,rate_per_day,send_window,max_attempts,retry_backoff_ms,retry_backoff_max_ms,message_ttl_seconds,track_delivery,
			ack_timeout_ms,sent_timeout_ms,retry_on_unknown,id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19) RETURNING `+policyCols,
			append(args, nid)...))
	} else {
		cur, gerr := s.GetPolicy(ctx, *id)
		if gerr != nil {
			return d, gerr
		}
		if cur.IsDefault != nil && *cur.IsDefault {
			// policy default harus tetap lengkap (CHECK di DB)
			if p.MinDelayMs == nil || p.MaxDelayMs == nil || p.SendWindow == nil {
				return d, Validation(FieldError{"name", "policy default harus lengkap (delay dan jendela waktu wajib)"})
			}
		}
		out, err = scanPolicy(s.Pool.QueryRow(ctx, `UPDATE dispatch_policies SET name=$1,min_delay_ms=$2,max_delay_ms=$3,burst_size=$4,burst_pause_ms=$5,
			max_inflight_per_sim=$6,rate_per_minute=$7,rate_per_hour=$8,rate_per_day=$9,send_window=$10,max_attempts=$11,retry_backoff_ms=$12,
			retry_backoff_max_ms=$13,message_ttl_seconds=$14,track_delivery=$15,ack_timeout_ms=$16,sent_timeout_ms=$17,retry_on_unknown=$18,updated_at=now()
			WHERE id=$19 RETURNING `+policyCols, append(args, *id)...))
	}
	if isUnique(err, "") {
		return d, NewProblem(http.StatusConflict, "resource.conflict", "Konflik", "nama policy sudah dipakai")
	}
	if err == nil {
		s.Wake()
	}
	return out, err
}

func (s *Service) DeletePolicy(ctx context.Context, id uuid.UUID) error {
	cur, err := s.GetPolicy(ctx, id)
	if err != nil {
		return err
	}
	if cur.IsDefault != nil && *cur.IsDefault {
		return NewProblem(http.StatusConflict, "policy.in_use", "Policy dipakai", "policy default tidak dapat dihapus")
	}
	_, err = s.Pool.Exec(ctx, `DELETE FROM dispatch_policies WHERE id=$1`, id)
	return err
}

func (s *Service) SetDefaultPolicy(ctx context.Context, id uuid.UUID) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE dispatch_policies SET is_default=false WHERE is_default`); err != nil {
			return err
		}
		ct, err := tx.Exec(ctx, `UPDATE dispatch_policies SET is_default=true WHERE id=$1`, id)
		if err != nil {
			return NewProblem(http.StatusUnprocessableEntity, "validation.failed", "Validasi gagal", "policy belum lengkap untuk dijadikan default: "+err.Error())
		}
		if ct.RowsAffected() == 0 {
			return NotFound("policy")
		}
		return nil
	})
}
