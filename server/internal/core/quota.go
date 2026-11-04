package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Kuota harian dalam satuan BAGIAN SMS (cerminan biaya: pesan 3 bagian = 3). Berlaku per API key (pesan lewat API) dan
// per pengguna dashboard (kirim cepat + campaign miliknya). Nilai per subjek: NULL = ikuti default env
// (DAILY_QUOTA_PER_KEY / DAILY_QUOTA_PER_USER), 0 = tanpa batas, N = batas. Hari dihitung di QUOTA_TIMEZONE.
// Pemakaian dicatat di quota_usage dalam transaksi yang sama dengan pembuatan pesan, sehingga dua permintaan
// bersamaan tidak dapat melampaui batas (baris kuota terkunci oleh UPSERT).

type QuotaSubject struct {
	Kind string // "key" | "user"
	ID   uuid.UUID
}

func (q QuotaSubject) key() string { return q.Kind + ":" + q.ID.String() }

// Quota: keadaan kuota satu subjek hari ini.
type Quota struct {
	DailyQuota *int      `json:"daily_quota"` // nilai tersimpan pada subjek (null = default)
	Effective  *int      `json:"effective"`   // batas yang berlaku (null = tanpa batas)
	Used       int       `json:"used"`        // bagian SMS terpakai hari ini
	Remaining  *int      `json:"remaining"`   // null = tanpa batas
	ResetsAt   time.Time `json:"resets_at"`   // awal hari berikutnya
	Timezone   string    `json:"timezone"`
	Unit       string    `json:"unit"` // "sms_parts"
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *Service) quotaLoc() *time.Location {
	if s.Cfg.QuotaLocation != nil {
		return s.Cfg.QuotaLocation
	}
	return time.UTC
}

// quotaDay: tanggal hari ini (zona kuota) dan awal hari berikutnya.
func (s *Service) quotaDay(now time.Time) (string, time.Time) {
	t := now.In(s.quotaLoc())
	y, m, d := t.Date()
	return t.Format("2006-01-02"), time.Date(y, m, d+1, 0, 0, 0, 0, s.quotaLoc())
}

// quotaLimit: batas efektif (nil = tanpa batas) dan nilai tersimpan.
func (s *Service) quotaLimit(ctx context.Context, db querier, q QuotaSubject) (eff *int, stored *int, err error) {
	table, def := "api_keys", s.Cfg.DailyQuotaKey
	if q.Kind == "user" {
		table, def = "users", s.Cfg.DailyQuotaUser
	}
	if err := db.QueryRow(ctx, `SELECT daily_quota FROM `+table+` WHERE id=$1`, q.ID).Scan(&stored); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil // subjek terhapus (mis. campaign tanpa pemilik): tidak dibatasi
		}
		return nil, nil, err
	}
	v := def
	if stored != nil {
		v = *stored
	}
	if v <= 0 {
		return nil, stored, nil
	}
	return &v, stored, nil
}

func (s *Service) quotaState(ctx context.Context, db querier, q QuotaSubject) (Quota, error) {
	day, next := s.quotaDay(time.Now())
	eff, stored, err := s.quotaLimit(ctx, db, q)
	if err != nil {
		return Quota{}, err
	}
	out := Quota{DailyQuota: stored, Effective: eff, ResetsAt: next, Timezone: s.quotaLoc().String(), Unit: "sms_parts"}
	if err := db.QueryRow(ctx, `SELECT COALESCE((SELECT used FROM quota_usage WHERE subject=$1 AND day=$2), 0)`, q.key(), day).Scan(&out.Used); err != nil {
		return Quota{}, err
	}
	if eff != nil {
		r := max(*eff-out.Used, 0)
		out.Remaining = &r
	}
	return out, nil
}

// QuotaOf: kuota subjek hari ini (untuk GET /quota dan daftar admin).
func (s *Service) QuotaOf(ctx context.Context, q QuotaSubject) (Quota, error) {
	return s.quotaState(ctx, s.Pool, q)
}

// quotaRemaining: sisa kuota (nil = tanpa batas), dibaca di dalam transaksi.
func (s *Service) quotaRemaining(ctx context.Context, tx pgx.Tx, q QuotaSubject) (*int, error) {
	st, err := s.quotaState(ctx, tx, q)
	return st.Remaining, err
}

// chargeQuota mencatat n bagian SMS (juga bila tanpa batas); gagal (dan transaksi pemanggil harus dibatalkan) bila melampaui batas.
func (s *Service) chargeQuota(ctx context.Context, tx pgx.Tx, q QuotaSubject, n int) error {
	if n <= 0 {
		return nil
	}
	eff, _, err := s.quotaLimit(ctx, tx, q)
	if err != nil {
		return err
	}
	day, next := s.quotaDay(time.Now())
	var used int
	if err := tx.QueryRow(ctx, `INSERT INTO quota_usage(subject, day, used) VALUES ($1, $2, $3)
		ON CONFLICT (subject, day) DO UPDATE SET used = quota_usage.used + EXCLUDED.used RETURNING used`, q.key(), day, n).Scan(&used); err != nil {
		return err
	}
	if eff != nil && used > *eff { // tanpa batas: pemakaian tetap dicatat untuk visibilitas
		s.M.QuotaRejections.WithLabelValues(q.Kind).Inc()
		p := NewProblem(http.StatusTooManyRequests, "quota.exceeded", "Kuota harian habis",
			fmt.Sprintf("permintaan ini butuh %d bagian SMS; terpakai %d dari %d hari ini; kuota pulih %s", n, used-n, *eff, next.Format("2006-01-02 15:04 MST")))
		p.RetryAfter = max(int(time.Until(next).Seconds()), 1)
		return p
	}
	return nil
}

// SetDailyQuota: admin menetapkan kuota (nil = ikuti default, 0 = tanpa batas).
func (s *Service) SetDailyQuota(ctx context.Context, q QuotaSubject, v *int, actor string) (Quota, error) {
	if v != nil && (*v < 0 || *v > 10_000_000) {
		return Quota{}, Validation(FieldError{"daily_quota", "0 (tanpa batas) sampai 10000000, atau null (ikuti default)"})
	}
	table, what := "api_keys", "API key"
	cond := " AND revoked_at IS NULL"
	if q.Kind == "user" {
		table, what, cond = "users", "pengguna", ""
	}
	ct, err := s.Pool.Exec(ctx, `UPDATE `+table+` SET daily_quota=$2 WHERE id=$1`+cond, q.ID, v)
	if err != nil {
		return Quota{}, err
	}
	if ct.RowsAffected() == 0 {
		return Quota{}, NotFound(what)
	}
	s.Audit(ctx, actor, "quota.set", q.Kind, q.ID.String(), map[string]any{"daily_quota": v})
	return s.QuotaOf(ctx, q)
}
