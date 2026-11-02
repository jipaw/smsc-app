package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/jipaw/smsc-app/server/internal/core"
)

type ctxKey int

const (
	keyPrincipal ctxKey = iota
	keyDevice
)

type principal struct {
	user     *core.User
	csrf     string
	apiKeyID uuid.UUID
	scopes   []string
}

func (p *principal) isKey() bool { return p.user == nil }

func (a *API) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *API) writeProblem(w http.ResponseWriter, r *http.Request, p *core.Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	out := map[string]any{"type": p.Type, "title": p.Title, "status": p.Status, "instance": r.URL.Path}
	if p.Detail != "" {
		out["detail"] = p.Detail
	}
	if len(p.Errors) > 0 {
		out["errors"] = p.Errors
	}
	_ = json.NewEncoder(w).Encode(out)
}

// fail memetakan error domain -> problem+json; error lain -> 500 (tanpa membocorkan detail).
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	var p *core.Problem
	if errors.As(err, &p) {
		a.writeProblem(w, r, p)
		return
	}
	a.Log.Error("kesalahan internal", "err", err, "path", r.URL.Path, "request_id", w.Header().Get("X-Request-Id"))
	a.writeProblem(w, r, core.NewProblem(http.StatusInternalServerError, "internal", "Kesalahan server", "lihat X-Request-Id"))
}

func (a *API) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		a.writeProblem(w, r, core.Validation(core.FieldError{Field: "body", Message: "JSON tidak valid: " + err.Error()}))
		return false
	}
	return true
}

// decodeMap membaca body sebagai peta mentah supaya "field tidak dikirim" dibedakan dari "null" (PATCH).
func (a *API) decodeMap(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	m := map[string]json.RawMessage{}
	if !a.decode(w, r, &m) {
		return nil, false
	}
	return m, true
}

func pathUUID(r *http.Request, name string) (uuid.UUID, *core.Problem) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, core.NotFound("resource")
	}
	return id, nil
}

func qUUID(r *http.Request, name string) (*uuid.UUID, *core.Problem) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return nil, core.Validation(core.FieldError{Field: name, Message: "bukan UUID"})
	}
	return &id, nil
}

func qInt(r *http.Request, name string, def int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get(name)); err == nil {
		return n
	}
	return def
}

func qTime(r *http.Request, name string) *time.Time {
	if t, err := time.Parse(time.RFC3339, r.URL.Query().Get(name)); err == nil {
		return &t
	}
	return nil
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// maskNumber: +628123457890 -> +6281****7890 (peran viewer, spec 09 §6)
func maskNumber(n string) string {
	if len(n) < 10 {
		return n
	}
	return n[:5] + "****" + n[len(n)-4:]
}

var e164Safe = regexp.MustCompile(`^\+[0-9]{7,15}$`)

// csvSafe mencegah formula injection pada ekspor (spec 09 T6); E.164 murni dibiarkan.
func csvSafe(s string) string {
	if s == "" || e164Safe.MatchString(s) {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// ---------- rate limit sederhana (token bucket per kunci) ----------

type limiter struct {
	mu    sync.Mutex
	b     map[string]*bucket
	rate  float64 // token per detik
	burst float64
}
type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(perSec, burst float64) *limiter {
	return &limiter{b: map[string]*bucket{}, rate: perSec, burst: burst}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	bk, ok := l.b[key]
	if !ok {
		bk = &bucket{tokens: l.burst, last: now}
		l.b[key] = bk
		if len(l.b) > 10000 { // bersihkan entri lama
			for k, v := range l.b {
				if now.Sub(v.last) > time.Hour {
					delete(l.b, k)
				}
			}
		}
	}
	bk.tokens += now.Sub(bk.last).Seconds() * l.rate
	if bk.tokens > l.burst {
		bk.tokens = l.burst
	}
	bk.last = now
	if bk.tokens < 1 {
		return false
	}
	bk.tokens--
	return true
}

func (a *API) limit(l *limiter, keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.allow(keyFn(r)) {
				w.Header().Set("Retry-After", "1")
				a.writeProblem(w, r, core.NewProblem(http.StatusTooManyRequests, "rate_limited", "Terlalu banyak permintaan", ""))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func principalOf(ctx context.Context) *principal {
	p, _ := ctx.Value(keyPrincipal).(*principal)
	return p
}
