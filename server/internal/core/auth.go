package core

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"
)

type User struct {
	ID             uuid.UUID `json:"id"`
	Email          string    `json:"email"`
	Role           string    `json:"role"`
	TailscaleLogin *string   `json:"tailscale_login"`
	MFAEnabled     bool      `json:"mfa_enabled"`
}

// Argon2id m=64MiB, t=3, p=1 (spec 09 §3).
const (
	argonMem  = 64 * 1024
	argonTime = 3
	argonPar  = 1
)

func HashPassword(pw string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	h := argon2.IDKey([]byte(pw), salt, argonTime, argonMem, argonPar, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMem, argonTime, argonPar,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h))
}

func VerifyPassword(pw, phc string) bool {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var dummyHash = HashPassword("dummy-untuk-waktu-konstan")

func (s *Service) CreateUser(ctx context.Context, email, password, role string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !strings.Contains(email, "@") {
		return User{}, Validation(FieldError{"email", "format email tidak valid"})
	}
	if role != "admin" && role != "operator" && role != "viewer" {
		return User{}, Validation(FieldError{"role", "harus admin, operator, atau viewer"})
	}
	if len(password) < 12 {
		return User{}, Validation(FieldError{"password", "minimal 12 karakter"})
	}
	u := User{ID: newID(), Email: email, Role: role}
	_, err := s.Pool.Exec(ctx, `INSERT INTO users(id,email,password_hash,role) VALUES ($1,$2,$3,$4)`, u.ID, email, HashPassword(password), role)
	if isUnique(err, "") {
		return User{}, NewProblem(http.StatusConflict, "resource.conflict", "Konflik", "email sudah terdaftar")
	}
	return u, err
}

var errBadLogin = NewProblem(http.StatusUnauthorized, "auth.invalid_credentials", "Kredensial salah", "email atau password salah")

// Login: pesan seragam dan waktu konstan (T14). Kunci akun 15 menit setelah 5 kegagalan.
// Bila 2FA aktif, hasilnya hanya token tantangan (MFARequired); sesi baru dibuat setelah LoginVerify.
func (s *Service) Login(ctx context.Context, email, password string) (LoginResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var u User
	var hash *string
	var failed int
	var locked *time.Time
	var disabled bool
	row := s.Pool.QueryRow(ctx, `SELECT id, email, role, tailscale_login, password_hash, failed_logins, locked_until, disabled, totp_secret IS NOT NULL FROM users WHERE lower(email)=$1`, email)
	scanErr := row.Scan(&u.ID, &u.Email, &u.Role, &u.TailscaleLogin, &hash, &failed, &locked, &disabled, &u.MFAEnabled)
	if scanErr != nil || hash == nil {
		VerifyPassword(password, dummyHash)
		return LoginResult{}, errBadLogin
	}
	if locked != nil && locked.After(time.Now()) {
		VerifyPassword(password, dummyHash)
		return LoginResult{}, NewProblem(http.StatusLocked, "auth.locked", "Akun terkunci", "terlalu banyak percobaan, coba lagi nanti")
	}
	if !VerifyPassword(password, *hash) || disabled {
		upd := `UPDATE users SET failed_logins = failed_logins+1, locked_until = CASE WHEN failed_logins+1 >= 5 THEN now() + interval '15 minutes' ELSE locked_until END WHERE id=$1`
		_, _ = s.Pool.Exec(ctx, upd, u.ID)
		return LoginResult{}, errBadLogin
	}
	if u.MFAEnabled {
		// penghitung gagal TIDAK direset di sini: percobaan menebak kode tetap terbatas 5x
		return LoginResult{User: u, MFARequired: true, MFAToken: s.mfaChallenge(u.ID, *hash)}, nil
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE users SET failed_logins=0, locked_until=NULL WHERE id=$1`, u.ID)
	return s.startSession(ctx, u)
}

func (s *Service) startSession(ctx context.Context, u User) (LoginResult, error) {
	token, csrf := randToken(32), randToken(24)
	_, err := s.Pool.Exec(ctx, `INSERT INTO sessions(id,user_id,token_hash,csrf_token,expires_at) VALUES ($1,$2,$3,$4,now() + interval '7 days')`,
		newID(), u.ID, s.hmacToken(token), csrf)
	return LoginResult{Token: token, CSRF: csrf, User: u}, err
}

// SessionUser memvalidasi cookie (kedaluwarsa geser 12 jam, absolut 7 hari) dan mengembalikan token CSRF.
func (s *Service) SessionUser(ctx context.Context, token string) (User, string, error) {
	var u User
	var csrf string
	err := s.Pool.QueryRow(ctx, `
		UPDATE sessions se SET last_seen_at = now()
		FROM users us
		WHERE se.user_id = us.id AND se.token_hash = ANY($1) AND se.expires_at > now()
		  AND se.last_seen_at > now() - interval '12 hours' AND NOT us.disabled
		RETURNING us.id, us.email, us.role, us.tailscale_login, us.totp_secret IS NOT NULL, se.csrf_token`, s.hmacTokens(token)).
		Scan(&u.ID, &u.Email, &u.Role, &u.TailscaleLogin, &u.MFAEnabled, &csrf)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", NewProblem(http.StatusUnauthorized, "auth.unauthenticated", "Tidak terautentikasi", "sesi tidak valid")
	}
	return u, csrf, err
}

func (s *Service) Logout(ctx context.Context, token string) {
	_, _ = s.Pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = ANY($1)`, s.hmacTokens(token))
}

// ---------- API key ----------

type APIKey struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
	Secret     string     `json:"secret,omitempty"`
	// rotasi: kapan terakhir diputar dan sampai kapan secret lama masih sah (null = sudah tidak berlaku)
	RotatedAt          *time.Time `json:"rotated_at"`
	PreviousValidUntil *time.Time `json:"previous_valid_until"`
}

func (s *Service) CreateAPIKey(ctx context.Context, by uuid.UUID, name string, scopes []string, exp *time.Time) (APIKey, error) {
	if strings.TrimSpace(name) == "" {
		return APIKey{}, Validation(FieldError{"name", "wajib diisi"})
	}
	if len(scopes) == 0 {
		scopes = []string{"messages:send", "messages:read"}
	}
	prefix := randToken(6)[:8]
	secret := "smsc_live_" + prefix + "_" + randToken(32)
	k := APIKey{ID: newID(), Name: name, Prefix: prefix, Scopes: scopes, ExpiresAt: exp, Secret: secret, CreatedAt: time.Now()}
	_, err := s.Pool.Exec(ctx, `INSERT INTO api_keys(id,name,prefix,key_hash,scopes,created_by,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		k.ID, name, prefix, sha(secret), scopes, by, exp)
	return k, err
}

// AuthAPIKey: kembalikan id key dan scope.
func (s *Service) AuthAPIKey(ctx context.Context, secret string) (uuid.UUID, []string, error) {
	var id uuid.UUID
	var scopes []string
	err := s.Pool.QueryRow(ctx, `
		UPDATE api_keys SET last_used_at = CASE WHEN last_used_at IS NULL OR last_used_at < now() - interval '1 minute' THEN now() ELSE last_used_at END
		WHERE (key_hash=$1 OR (prev_key_hash=$1 AND prev_valid_until > now())) AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now()) RETURNING id, scopes`, sha(secret)).Scan(&id, &scopes)
	if err != nil {
		return uuid.Nil, nil, NewProblem(http.StatusUnauthorized, "auth.unauthenticated", "Tidak terautentikasi", "API key tidak valid")
	}
	return id, scopes, nil
}

func (s *Service) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,name,prefix,scopes,last_used_at,expires_at,created_at,rotated_at,CASE WHEN prev_valid_until > now() THEN prev_valid_until END FROM api_keys WHERE revoked_at IS NULL ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (APIKey, error) {
		var k APIKey
		err := r.Scan(&k.ID, &k.Name, &k.Prefix, &k.Scopes, &k.LastUsedAt, &k.ExpiresAt, &k.CreatedAt, &k.RotatedAt, &k.PreviousValidUntil)
		return k, err
	})
}

// MaxRotateGrace: batas masa tumpang tindih secret lama (API key dan secret webhook).
const MaxRotateGrace = 7 * 24 * 3600

func graceOK(sec int) *FieldError {
	if sec < 0 || sec > MaxRotateGrace {
		return &FieldError{"grace_seconds", "0 sampai 604800 (7 hari)"}
	}
	return nil
}

// RotateAPIKey menerbitkan secret baru untuk key yang sama (id, nama, scope tetap). Secret lama tetap sah selama
// graceSeconds (0 = langsung mati) agar sistem klien sempat diganti tanpa downtime.
func (s *Service) RotateAPIKey(ctx context.Context, id uuid.UUID, graceSeconds int) (APIKey, error) {
	if fe := graceOK(graceSeconds); fe != nil {
		return APIKey{}, Validation(*fe)
	}
	prefix := randToken(6)[:8]
	secret := "smsc_live_" + prefix + "_" + randToken(32)
	var k APIKey
	err := s.Pool.QueryRow(ctx, `UPDATE api_keys SET
		  prev_key_hash    = CASE WHEN $4 > 0 THEN key_hash END,
		  prev_valid_until = CASE WHEN $4 > 0 THEN now() + make_interval(secs => $4) END,
		  key_hash=$2, prefix=$3, rotated_at=now()
		WHERE id=$1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
		RETURNING id,name,prefix,scopes,last_used_at,expires_at,created_at,rotated_at,prev_valid_until`,
		id, sha(secret), prefix, graceSeconds).
		Scan(&k.ID, &k.Name, &k.Prefix, &k.Scopes, &k.LastUsedAt, &k.ExpiresAt, &k.CreatedAt, &k.RotatedAt, &k.PreviousValidUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, NotFound("API key")
	}
	k.Secret = secret
	return k, err
}

func (s *Service) RevokeAPIKey(ctx context.Context, id uuid.UUID) error {
	ct, err := s.Pool.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, id)
	if err == nil && ct.RowsAffected() == 0 {
		return NotFound("API key")
	}
	return err
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,email,role,tailscale_login,totp_secret IS NOT NULL FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) {
		var u User
		return u, r.Scan(&u.ID, &u.Email, &u.Role, &u.TailscaleLogin, &u.MFAEnabled)
	})
}
