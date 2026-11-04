package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Kebijakan wajib-2FA (fase 5). Default "off": 2FA tetap opsional. "admin": semua admin wajib; "all": semua pengguna.
// Pengguna dalam cakupan yang belum punya 2FA tetap bisa login tetapi hanya dapat membuka endpoint akun
// (/auth/me, /auth/logout, /auth/password, /auth/2fa*) sampai 2FA aktif; API key tidak terpengaruh.
const (
	Require2FAOff   = "off"
	Require2FAAdmin = "admin"
	Require2FAAll   = "all"
)

type SecuritySettings struct {
	Require2FA      string `json:"require_2fa"`
	PasskeyLogin    bool   `json:"passkey_login"`     // izinkan login tanpa password dengan passkey (default mati)
	UsersMissing2FA int    `json:"users_missing_2fa"` // pengguna aktif dalam cakupan yang belum punya 2FA
}

type secCache struct {
	mu      sync.Mutex
	loaded  bool
	level   string
	passkey bool
	at      time.Time
}

// secPolicy membaca kebijakan keamanan (cache 2 dtk). Gagal membaca → fail-open untuk 2FA wajib (jangan mengunci semua orang)
// DAN fail-closed untuk login passkey (fitur opsional yang melewati password).
func (s *Service) secPolicy(ctx context.Context) (level string, passkey bool) {
	s.sec.mu.Lock()
	defer s.sec.mu.Unlock()
	if s.sec.loaded && time.Since(s.sec.at) < 2*time.Second {
		return s.sec.level, s.sec.passkey
	}
	level = Require2FAOff
	var raw []byte
	if err := s.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE key='security'`).Scan(&raw); err == nil {
		var v struct {
			Require2FA   string `json:"require_2fa"`
			PasskeyLogin bool   `json:"passkey_login"`
		}
		if json.Unmarshal(raw, &v) == nil {
			if v.Require2FA == Require2FAAdmin || v.Require2FA == Require2FAAll {
				level = v.Require2FA
			}
			passkey = v.PasskeyLogin
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Require2FAOff, false
	}
	s.sec.loaded, s.sec.level, s.sec.passkey, s.sec.at = true, level, passkey, time.Now()
	return level, passkey
}

func (s *Service) mfaLevel(ctx context.Context) string { l, _ := s.secPolicy(ctx); return l }

// PasskeyLoginAllowed: admin mengizinkan login tanpa password dengan passkey.
func (s *Service) PasskeyLoginAllowed(ctx context.Context) bool { _, p := s.secPolicy(ctx); return p }

func inScope(level, role string) bool {
	return level == Require2FAAll || (level == Require2FAAdmin && role == "admin")
}

// MFASetupRequired: pengguna ini wajib menyiapkan 2FA sebelum memakai dashboard.
func (s *Service) MFASetupRequired(ctx context.Context, u User) bool {
	if u.MFAEnabled || !inScope(s.mfaLevel(ctx), u.Role) {
		return false
	}
	// akun SSO-saja (tanpa password) tidak dapat menyiapkan 2FA lokal — MFA menjadi tanggung jawab IdP
	if !u.HasPassword {
		return false
	}
	// sesi SSO memenuhi kewajiban bila admin menyatakan IdP menegakkan MFA (OIDC_TRUST_IDP_MFA, default true)
	if u.SSOSession && s.Cfg.OIDC.TrustIdPMFA {
		return false
	}
	return true
}

func (s *Service) GetSecurity(ctx context.Context) (SecuritySettings, error) {
	s.sec.mu.Lock()
	s.sec.loaded = false // paksa baca ulang
	s.sec.mu.Unlock()
	lvl, pk := s.secPolicy(ctx)
	out := SecuritySettings{Require2FA: lvl, PasskeyLogin: pk}
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE NOT disabled AND NOT `+mfaAny("users")+`
		AND ($1='all' OR ($1='admin' AND role='admin'))`, out.Require2FA).Scan(&out.UsersMissing2FA)
	return out, err
}

// SetSecurity mengubah kebijakan; nil = tidak diubah. Menyalakan kewajiban 2FA ditolak bila admin yang meminta sendiri belum
// memakai 2FA (mencegah mengunci diri sendiri); pengguna lain baru dipaksa pada permintaan berikutnya.
func (s *Service) SetSecurity(ctx context.Context, actor uuid.UUID, level *string, passkey *bool) (SecuritySettings, error) {
	if level == nil && passkey == nil {
		return SecuritySettings{}, Validation(FieldError{"body", "isi minimal salah satu: require_2fa, passkey_login"})
	}
	curLevel, curPasskey := s.secPolicy(ctx)
	if level != nil {
		l := *level
		if l != Require2FAOff && l != Require2FAAdmin && l != Require2FAAll {
			return SecuritySettings{}, Validation(FieldError{"require_2fa", "harus off, admin, atau all"})
		}
		if l != Require2FAOff {
			st, err := s.MFAStatus(ctx, actor)
			if err != nil {
				return SecuritySettings{}, err
			}
			if !st.Enabled {
				return SecuritySettings{}, NewProblem(http.StatusConflict, "security.enroll_first", "Aktifkan 2FA Anda dahulu",
					"admin yang menyalakan kewajiban 2FA harus sudah memakai 2FA, agar tidak terkunci sendiri")
			}
		}
		curLevel = l
	}
	if passkey != nil {
		curPasskey = *passkey
	}
	raw, _ := json.Marshal(map[string]any{"require_2fa": curLevel, "passkey_login": curPasskey})
	if _, err := s.Pool.Exec(ctx, `INSERT INTO settings(key,value) VALUES ('security',$1) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`, raw); err != nil {
		return SecuritySettings{}, err
	}
	return s.GetSecurity(ctx)
}

// ---------- manajemen pengguna ----------

type UserPatch struct {
	Role     *string
	Disabled *bool
}

// UpdateUser mengubah peran / status aktif pengguna lain. Mengubah diri sendiri ditolak (409): ini sekaligus menjamin
// selalu ada admin aktif — pelaku adalah admin aktif dan bukan targetnya. Menonaktifkan mencabut semua sesi target.
func (s *Service) UpdateUser(ctx context.Context, actor, id uuid.UUID, p UserPatch) (UserItem, map[string]any, error) {
	if p.Role == nil && p.Disabled == nil {
		return UserItem{}, nil, Validation(FieldError{"body", "isi minimal salah satu: role, disabled"})
	}
	if p.Role != nil && *p.Role != "admin" && *p.Role != "operator" && *p.Role != "viewer" {
		return UserItem{}, nil, Validation(FieldError{"role", "harus admin, operator, atau viewer"})
	}
	if id == actor {
		return UserItem{}, nil, NewProblem(http.StatusConflict, "user.self_change", "Tidak dapat mengubah diri sendiri", "minta admin lain mengubah peran atau status akun Anda")
	}
	var oldRole string
	var oldDisabled bool
	if err := s.Pool.QueryRow(ctx, `SELECT role::text, disabled FROM users WHERE id=$1`, id).Scan(&oldRole, &oldDisabled); err != nil {
		return UserItem{}, nil, NotFound("pengguna")
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE users SET role=COALESCE($2::user_role, role), disabled=COALESCE($3, disabled) WHERE id=$1`, id, p.Role, p.Disabled); err != nil {
		return UserItem{}, nil, err
	}
	change := map[string]any{}
	if p.Role != nil && *p.Role != oldRole {
		change["role"] = map[string]string{"from": oldRole, "to": *p.Role}
	}
	if p.Disabled != nil && *p.Disabled != oldDisabled {
		change["disabled"] = *p.Disabled
	}
	if p.Disabled != nil && *p.Disabled {
		if _, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id); err != nil {
			return UserItem{}, nil, err
		}
	}
	var u UserItem
	err := s.Pool.QueryRow(ctx, `SELECT id,email,role,tailscale_login,`+mfaAny("users")+`,password_hash IS NOT NULL,oidc_subject IS NOT NULL,disabled,created_at,scim_managed FROM users WHERE id=$1`, id).
		Scan(&u.ID, &u.Email, &u.Role, &u.TailscaleLogin, &u.MFAEnabled, &u.HasPassword, &u.SSOLinked, &u.Disabled, &u.CreatedAt, &u.SCIMManaged)
	return u, change, err
}

// AdminSetPassword: admin menetapkan password baru untuk pengguna lain (lupa password). Semua sesi targetnya dicabut
// dan penghitung kunci akun direset. Untuk diri sendiri gunakan ChangePassword.
func (s *Service) AdminSetPassword(ctx context.Context, actor, id uuid.UUID, password string) error {
	if id == actor {
		return NewProblem(http.StatusConflict, "user.self_change", "Gunakan Ganti password", "ganti password sendiri lewat /auth/password")
	}
	var email string
	if err := s.Pool.QueryRow(ctx, `SELECT email FROM users WHERE id=$1`, id).Scan(&email); err != nil {
		return NotFound("pengguna")
	}
	if err := checkPassword("new_password", password, email); err != nil {
		return err
	}
	ct, err := s.Pool.Exec(ctx, `UPDATE users SET password_hash=$2, password_changed_at=now(), failed_logins=0, locked_until=NULL WHERE id=$1`, id, HashPassword(password))
	if err != nil {
		return err
	}
	_, _ = s.Pool.Exec(ctx, `DELETE FROM login_failures WHERE user_id=$1`, id)
	if ct.RowsAffected() == 0 {
		return NotFound("pengguna")
	}
	_, err = s.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id)
	return err
}
