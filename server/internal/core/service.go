// Package core: logika domain server (auth, device, pesan, dispatcher, reaper). Semua akses DB lewat sini.
package core

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jipaw/smsc-app/server/internal/bus"
	"github.com/jipaw/smsc-app/server/internal/config"
	"github.com/jipaw/smsc-app/server/internal/proto"
)

var ErrOffline = errors.New("device offline")

// DeviceLink: batas ke WS hub (diimplementasikan paket hub).
type DeviceLink interface {
	Send(deviceID uuid.UUID, env proto.Envelope) error
	Request(ctx context.Context, deviceID uuid.UUID, env proto.Envelope, timeout time.Duration) (proto.Envelope, error)
	Connected(deviceID uuid.UUID) bool
	Close(deviceID uuid.UUID, code int, reason string)
}

type Service struct {
	Pool *pgxpool.Pool
	Cfg  config.Config
	Log  *slog.Logger
	Bus  *bus.Bus

	link    DeviceLink
	wake    chan struct{}
	cmdRefs sync.Map // id envelope sms.send -> uuid pesan
}

func New(pool *pgxpool.Pool, cfg config.Config, log *slog.Logger, b *bus.Bus) *Service {
	return &Service{Pool: pool, Cfg: cfg, Log: log, Bus: b, wake: make(chan struct{}, 1)}
}

func (s *Service) SetLink(l DeviceLink) { s.link = l }

// Wake membangunkan dispatcher tanpa memblokir.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Problem: error HTTP terstruktur (RFC 9457) yang dikembalikan lapisan domain.
type Problem struct {
	Type   string       `json:"type"`
	Title  string       `json:"title"`
	Status int          `json:"status"`
	Detail string       `json:"detail,omitempty"`
	Errors []FieldError `json:"errors,omitempty"`
}
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (p *Problem) Error() string { return p.Type + ": " + p.Detail }

func NewProblem(status int, typ, title, detail string) *Problem {
	return &Problem{Type: typ, Title: title, Status: status, Detail: detail}
}
func NotFound(what string) *Problem {
	return NewProblem(http.StatusNotFound, "resource.not_found", "Tidak ditemukan", what+" tidak ditemukan")
}
func Validation(errs ...FieldError) *Problem {
	return &Problem{Type: "validation.failed", Title: "Validasi gagal", Status: http.StatusUnprocessableEntity, Errors: errs,
		Detail: func() string {
			if len(errs) > 0 {
				return errs[0].Field + ": " + errs[0].Message
			}
			return ""
		}()}
}

// ---------- util ----------

func (s *Service) withTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func newID() uuid.UUID { id, _ := uuid.NewV7(); return id }

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

func (s *Service) hmacToken(t string) []byte {
	m := hmac.New(sha256.New, []byte(s.Cfg.SessionSecret))
	m.Write([]byte(t))
	return m.Sum(nil)
}

func isUnique(err error, constraint string) bool {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return constraint == "" || pe.ConstraintName == constraint
	}
	return false
}

func ptr[T any](v T) *T { return &v }

func fmtErr(format string, a ...any) error { return fmt.Errorf(format, a...) }

// Audit mencatat tindakan sensitif (append-only).
func (s *Service) Audit(ctx context.Context, actor, action, targetType, targetID string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO audit_logs(id, actor, action, target_type, target_id, data) VALUES ($1,$2,$3,$4,$5,$6)`,
		newID(), actor, action, targetType, targetID, data)
	if err != nil {
		s.Log.Warn("audit gagal", "err", err)
	}
}
