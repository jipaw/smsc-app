package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	mfaTokenTTL   = 5 * time.Minute
	recoveryCount = 10
	totpIssuer    = "SMSC Gateway"
)

var (
	errBadCode = NewProblem(http.StatusUnauthorized, "auth.invalid_code", "Kode salah", "kode verifikasi atau kode pemulihan salah")
	sixDigits  = regexp.MustCompile(`^\d{6}$`)
)

// LoginResult: sesi penuh, atau (MFARequired) token tantangan berumur 5 menit yang hanya berlaku untuk /auth/login/verify.
type LoginResult struct {
	Token, CSRF string
	User        User
	MFARequired bool
	MFAToken    string
	Methods     []string // metode tersedia untuk tantangan: webauthn|totp|recovery
}

func (s *Service) mfaMAC(payload string) []byte {
	m := hmac.New(sha256.New, []byte(s.Cfg.SessionSecret))
	m.Write([]byte("mfa-challenge|" + payload))
	return m.Sum(nil)
}

// mfaChallenge: token tanpa status (uid|exp|sidik jari password). Mengganti password membatalkan tantangan yang beredar.
func (s *Service) mfaChallenge(uid uuid.UUID, pwHash string) string {
	payload := fmt.Sprintf("%s|%d|%x", uid, time.Now().Add(mfaTokenTTL).Unix(), sha(pwHash)[:8])
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(s.mfaMAC(payload))
}

func (s *Service) parseChallenge(tok string) (uid uuid.UUID, fp string, err error) {
	bad := NewProblem(http.StatusUnauthorized, "auth.mfa_expired", "Tantangan kedaluwarsa", "ulangi login dari awal")
	a, b, ok := strings.Cut(tok, ".")
	if !ok {
		return uuid.Nil, "", bad
	}
	pl, err1 := base64.RawURLEncoding.DecodeString(a)
	mac, err2 := base64.RawURLEncoding.DecodeString(b)
	if err1 != nil || err2 != nil || !hmac.Equal(mac, s.mfaMAC(string(pl))) {
		return uuid.Nil, "", bad
	}
	parts := strings.Split(string(pl), "|")
	if len(parts) != 3 {
		return uuid.Nil, "", bad
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	id, err2x := uuid.Parse(parts[0])
	if err != nil || err2x != nil || time.Now().Unix() > exp {
		return uuid.Nil, "", bad
	}
	return id, parts[2], nil
}

// mfaAny: ekspresi SQL "pengguna memakai 2FA" = TOTP aktif ATAU punya ≥ 1 kunci keamanan/passkey. alias = nama tabel/alias users.
func mfaAny(alias string) string {
	return "(" + alias + ".totp_secret IS NOT NULL OR EXISTS (SELECT 1 FROM webauthn_credentials wc WHERE wc.user_id = " + alias + ".id))"
}

// challengeUser memvalidasi token tantangan dan memuat penggunanya (tidak terkunci, aktif, password belum berubah, 2FA aktif).
func (s *Service) challengeUser(ctx context.Context, mfaToken string) (User, error) {
	uid, fp, err := s.parseChallenge(mfaToken)
	if err != nil {
		return User{}, err
	}
	var u User
	var hash *string
	var locked *time.Time
	var disabled bool
	err = s.Pool.QueryRow(ctx, `SELECT id,email,role,tailscale_login,password_hash,locked_until,disabled,`+mfaAny("users")+` FROM users WHERE id=$1`, uid).
		Scan(&u.ID, &u.Email, &u.Role, &u.TailscaleLogin, &hash, &locked, &disabled, &u.MFAEnabled)
	if err != nil || hash == nil || disabled || !u.MFAEnabled || fmt.Sprintf("%x", sha(*hash)[:8]) != fp {
		return User{}, NewProblem(http.StatusUnauthorized, "auth.mfa_expired", "Tantangan kedaluwarsa", "ulangi login dari awal")
	}
	if locked != nil && locked.After(time.Now()) {
		return User{}, NewProblem(http.StatusLocked, "auth.locked", "Akun terkunci", "terlalu banyak percobaan, coba lagi nanti")
	}
	u.HasPassword = true // lolos langkah password
	return u, nil
}

// failSecondFactor menghitung kegagalan faktor kedua seperti kegagalan password (5x → kunci 15 menit).
func (s *Service) failSecondFactor(ctx context.Context, uid uuid.UUID) {
	_, _ = s.Pool.Exec(ctx, `UPDATE users SET failed_logins = failed_logins+1, locked_until = CASE WHEN failed_logins+1 >= 5 THEN now() + interval '15 minutes' ELSE locked_until END WHERE id=$1`, uid)
}

// LoginVerify menyelesaikan login tahap kedua dengan kode TOTP atau kode pemulihan.
func (s *Service) LoginVerify(ctx context.Context, mfaToken, code string) (LoginResult, error) {
	u, err := s.challengeUser(ctx, mfaToken)
	if err != nil {
		return LoginResult{}, err
	}
	ok, kind := s.checkSecondFactor(ctx, u.ID, code)
	if !ok {
		s.failSecondFactor(ctx, u.ID)
		return LoginResult{}, errBadCode
	}
	return s.finishSecondFactor(ctx, u, kind)
}

func (s *Service) finishSecondFactor(ctx context.Context, u User, kind string) (LoginResult, error) {
	_, _ = s.Pool.Exec(ctx, `UPDATE users SET failed_logins=0, locked_until=NULL WHERE id=$1`, u.ID)
	res, err := s.startSession(ctx, u)
	if err == nil && kind == "recovery" {
		s.Audit(ctx, "user:"+u.Email, "auth.recovery_code_used", "user", u.ID.String(), nil)
	}
	return res, err
}

// checkSecondFactor memakai kode TOTP (6 digit, sekali pakai per langkah) atau kode pemulihan (sekali pakai).
func (s *Service) checkSecondFactor(ctx context.Context, uid uuid.UUID, code string) (bool, string) {
	code = strings.TrimSpace(code)
	if sixDigits.MatchString(code) {
		var enc []byte
		var last int64
		if s.Pool.QueryRow(ctx, `SELECT totp_secret, totp_last_step FROM users WHERE id=$1`, uid).Scan(&enc, &last) != nil || enc == nil {
			return false, ""
		}
		secret, _, err := s.open(purposeTOTP, enc)
		if err != nil {
			s.Log.Error("rahasia TOTP tidak dapat dibuka", "user", uid)
			return false, ""
		}
		step := verifyTOTP(secret, code, time.Now(), last)
		if step == 0 {
			return false, ""
		}
		// maju secara atomik: dua permintaan paralel dengan kode sama, hanya satu yang menang
		ct, err := s.Pool.Exec(ctx, `UPDATE users SET totp_last_step=$2 WHERE id=$1 AND totp_last_step<$2`, uid, step)
		return err == nil && ct.RowsAffected() == 1, "totp"
	}
	rc := normalizeRecovery(code)
	if len(rc) != 10 {
		return false, ""
	}
	ct, err := s.Pool.Exec(ctx, `UPDATE user_recovery_codes SET used_at=now() WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL`, uid, recoveryHash(uid, rc))
	return err == nil && ct.RowsAffected() == 1, "recovery"
}

func recoveryHash(uid uuid.UUID, normalized string) []byte {
	return sha(uid.String() + "|" + normalized)
}

// ---------- pengelolaan 2FA oleh pengguna ----------

type MFAStatus struct {
	Enabled           bool       `json:"enabled"` // faktor apa pun (TOTP atau kunci keamanan)
	TOTPEnabled       bool       `json:"totp_enabled"`
	WebAuthnCount     int        `json:"webauthn_credentials"`
	EnabledAt         *time.Time `json:"enabled_at"`
	RecoveryRemaining int        `json:"recovery_codes_remaining"`
}

func (s *Service) MFAStatus(ctx context.Context, uid uuid.UUID) (MFAStatus, error) {
	var st MFAStatus
	err := s.Pool.QueryRow(ctx, `SELECT `+mfaAny("u")+`, u.totp_secret IS NOT NULL,
		(SELECT count(*) FROM webauthn_credentials w WHERE w.user_id=u.id),
		LEAST(u.totp_enabled_at, (SELECT min(w.created_at) FROM webauthn_credentials w WHERE w.user_id=u.id)),
		(SELECT count(*) FROM user_recovery_codes r WHERE r.user_id=u.id AND r.used_at IS NULL) FROM users u WHERE u.id=$1`, uid).
		Scan(&st.Enabled, &st.TOTPEnabled, &st.WebAuthnCount, &st.EnabledAt, &st.RecoveryRemaining)
	return st, err
}

func (s *Service) verifyPassword(ctx context.Context, uid uuid.UUID, pw string) error {
	var hash *string
	if s.Pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, uid).Scan(&hash) != nil || hash == nil || !VerifyPassword(pw, *hash) {
		return NewProblem(http.StatusUnauthorized, "auth.invalid_credentials", "Password salah", "password saat ini salah")
	}
	return nil
}

type MFASetup struct {
	Secret     string `json:"secret"`
	OtpauthURI string `json:"otpauth_uri"`
}

// MFASetupStart menyiapkan rahasia baru (belum aktif sampai dikonfirmasi dengan kode). Memanggil ulang mengganti rahasia tertunda.
func (s *Service) MFASetupStart(ctx context.Context, uid uuid.UUID, email, password string) (MFASetup, error) {
	if err := s.verifyPassword(ctx, uid, password); err != nil {
		return MFASetup{}, err
	}
	st, err := s.MFAStatus(ctx, uid)
	if err != nil {
		return MFASetup{}, err
	}
	if st.TOTPEnabled {
		return MFASetup{}, NewProblem(http.StatusConflict, "mfa.already_enabled", "TOTP sudah aktif", "nonaktifkan 2FA dahulu untuk mengganti aplikasi authenticator")
	}
	secret := newTOTPSecret()
	enc, err := s.seal(purposeTOTP, secret)
	if err != nil {
		return MFASetup{}, err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE users SET totp_pending=$2 WHERE id=$1`, uid, enc); err != nil {
		return MFASetup{}, err
	}
	return MFASetup{Secret: secret, OtpauthURI: otpauthURI(totpIssuer, email, secret)}, nil
}

// MFAEnable mengonfirmasi rahasia tertunda dengan kode pertama, mengaktifkan 2FA, dan menerbitkan kode pemulihan (tampil sekali).
func (s *Service) MFAEnable(ctx context.Context, uid uuid.UUID, code string) ([]string, error) {
	var pend []byte
	if err := s.Pool.QueryRow(ctx, `SELECT totp_pending FROM users WHERE id=$1 AND totp_secret IS NULL`, uid).Scan(&pend); err != nil || pend == nil {
		return nil, NewProblem(http.StatusConflict, "mfa.no_pending_setup", "Belum ada penyiapan", "mulai dari /auth/2fa/setup")
	}
	secret, _, err := s.open(purposeTOTP, pend)
	if err != nil {
		return nil, err
	}
	step := verifyTOTP(secret, strings.TrimSpace(code), time.Now(), 0)
	if step == 0 {
		return nil, Validation(FieldError{"code", "kode salah; pastikan jam HP akurat"})
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// kode pemulihan hanya diterbitkan saat faktor 2FA PERTAMA aktif; faktor tambahan memakai kode yang sudah ada
	var hadMFA bool
	if err := tx.QueryRow(ctx, `SELECT `+mfaAny("users")+` FROM users WHERE id=$1 FOR UPDATE`, uid).Scan(&hadMFA); err != nil {
		return nil, err
	}
	codes := []string{}
	if _, err := tx.Exec(ctx, `UPDATE users SET totp_secret=totp_pending, totp_pending=NULL, totp_enabled_at=now(), totp_last_step=$2 WHERE id=$1`, uid, step); err != nil {
		return nil, err
	}
	if !hadMFA {
		var hashes [][]byte
		codes, hashes = newRecoverySet(uid)
		if err := replaceRecovery(ctx, tx, uid, hashes); err != nil {
			return nil, err
		}
	}
	return codes, tx.Commit(ctx)
}

func newRecoverySet(uid uuid.UUID) (codes []string, hashes [][]byte) {
	for i := 0; i < recoveryCount; i++ {
		c := newRecoveryCode()
		codes = append(codes, c)
		hashes = append(hashes, recoveryHash(uid, normalizeRecovery(c)))
	}
	return
}

func replaceRecovery(ctx context.Context, tx pgx.Tx, uid uuid.UUID, hashes [][]byte) error {
	if _, err := tx.Exec(ctx, `DELETE FROM user_recovery_codes WHERE user_id=$1`, uid); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.Exec(ctx, `INSERT INTO user_recovery_codes(user_id, code_hash) VALUES ($1,$2)`, uid, h); err != nil {
			return err
		}
	}
	return nil
}

// reauth: password + faktor kedua, untuk operasi sensitif pada 2FA (nonaktifkan, terbitkan ulang kode).
func (s *Service) reauth(ctx context.Context, uid uuid.UUID, password, code string) error {
	if err := s.verifyPassword(ctx, uid, password); err != nil {
		return err
	}
	st, err := s.MFAStatus(ctx, uid)
	if err != nil {
		return err
	}
	if !st.Enabled {
		return NewProblem(http.StatusConflict, "mfa.not_enabled", "2FA belum aktif", "tidak ada yang perlu diubah")
	}
	if ok, _ := s.checkSecondFactor(ctx, uid, code); !ok {
		return errBadCode
	}
	return nil
}

func (s *Service) MFADisable(ctx context.Context, uid uuid.UUID, password, code string) error {
	if err := s.reauth(ctx, uid, password, code); err != nil {
		return err
	}
	return s.clearMFA(ctx, uid)
}

func (s *Service) MFARegenerateCodes(ctx context.Context, uid uuid.UUID, password, code string) ([]string, error) {
	if err := s.reauth(ctx, uid, password, code); err != nil {
		return nil, err
	}
	codes, hashes := newRecoverySet(uid)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := replaceRecovery(ctx, tx, uid, hashes); err != nil {
		return nil, err
	}
	return codes, tx.Commit(ctx)
}

func (s *Service) clearMFA(ctx context.Context, uid uuid.UUID) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `UPDATE users SET totp_secret=NULL, totp_pending=NULL, totp_enabled_at=NULL, totp_last_step=0 WHERE id=$1`, uid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_recovery_codes WHERE user_id=$1`, uid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM webauthn_credentials WHERE user_id=$1`, uid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM webauthn_ceremonies WHERE user_id=$1`, uid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MFAReset: admin mereset 2FA pengguna yang kehilangan perangkat. Semua sesi pengguna itu dicabut.
func (s *Service) MFAReset(ctx context.Context, uid uuid.UUID) error {
	var n int
	if err := s.Pool.QueryRow(ctx, `SELECT 1 FROM users WHERE id=$1`, uid).Scan(&n); errors.Is(err, pgx.ErrNoRows) {
		return NotFound("pengguna")
	}
	if err := s.clearMFA(ctx, uid); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, uid)
	return err
}

// ResetMFAByEmail: untuk CLI (admin terkunci keluar).
func (s *Service) ResetMFAByEmail(ctx context.Context, email string) (uuid.UUID, error) {
	var id uuid.UUID
	if err := s.Pool.QueryRow(ctx, `SELECT id FROM users WHERE lower(email)=lower($1)`, strings.TrimSpace(email)).Scan(&id); err != nil {
		return uuid.Nil, NotFound("pengguna")
	}
	return id, s.MFAReset(ctx, id)
}

// ChangePassword mengganti password sendiri; semua sesi lain pengguna dicabut (sesi saat ini dipertahankan).
func (s *Service) ChangePassword(ctx context.Context, uid uuid.UUID, currentToken, current, next string) error {
	if err := s.verifyPassword(ctx, uid, current); err != nil {
		return err
	}
	var email string
	_ = s.Pool.QueryRow(ctx, `SELECT email FROM users WHERE id=$1`, uid).Scan(&email)
	if err := checkPassword("new_password", next, email); err != nil {
		return err
	}
	if next == current {
		return Validation(FieldError{"new_password", "harus berbeda dari password saat ini"})
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE users SET password_hash=$2, password_changed_at=now(), failed_logins=0, locked_until=NULL WHERE id=$1`, uid, HashPassword(next)); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1 AND NOT (token_hash = ANY($2))`, uid, s.hmacTokens(currentToken))
	return err
}

// SetPasswordByEmail: untuk CLI (lupa password). Semua sesi pengguna dicabut.
func (s *Service) SetPasswordByEmail(ctx context.Context, email, password string) (uuid.UUID, error) {
	if err := checkPassword("password", password, email); err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err := s.Pool.QueryRow(ctx, `UPDATE users SET password_hash=$2, password_changed_at=now(), failed_logins=0, locked_until=NULL WHERE lower(email)=lower($1) RETURNING id`,
		strings.TrimSpace(email), HashPassword(password)).Scan(&id)
	if err != nil {
		return uuid.Nil, NotFound("pengguna")
	}
	_, _ = s.Pool.Exec(ctx, `DELETE FROM login_failures WHERE user_id=$1`, id) // reset oleh admin juga membuka kunci per sumber
	_, err = s.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id)
	return id, err
}
