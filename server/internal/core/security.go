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
	UsersMissing2FA int    `json:"users_missing_2fa"` // pengguna aktif dalam cakupan yang belum punya 2FA
}

type secCache struct {
	mu    sync.Mutex
	level string
	at    time.Time
}

func (s *Service) mfaLevel(ctx context.Context) string {
	s.sec.mu.Lock()
	defer s.sec.mu.Unlock()
	if s.sec.level != "" && time.Since(s.sec.at) < 2*time.Second {
		return s.sec.level
	}
	level := Require2FAOff
	var raw []byte
	if err := s.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE key='security'`).Scan(&raw); err == nil {
		var v struct {
			Require2FA string `json:"require_2fa"`
		}
		if json.Unmarshal(raw, &v) == nil && (v.Require2FA == Require2FAAdmin || v.Require2FA == Require2FAAll) {
			level = v.Require2FA
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Require2FAOff // gagal membaca: jangan mengunci semua orang
	}
	s.sec.level, s.sec.at = level, time.Now()
	return level
}

func inScope(level, role string) bool {
	return level == Require2FAAll || (level == Require2FAAdmin && role == "admin")
}

// MFASetupRequired: pengguna ini wajib menyiapkan 2FA sebelum memakai dashboard.
func (s *Service) MFASetupRequired(ctx context.Context, u User) bool {
	return !u.MFAEnabled && inScope(s.mfaLevel(ctx), u.Role)
}

func (s *Service) GetSecurity(ctx context.Context) (SecuritySettings, error) {
	s.sec.mu.Lock()
	s.sec.level = "" // paksa baca ulang
	s.sec.mu.Unlock()
	out := SecuritySettings{Require2FA: s.mfaLevel(ctx)}
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE NOT disabled AND totp_secret IS NULL
		AND ($1='all' OR ($1='admin' AND role='admin'))`, out.Require2FA).Scan(&out.UsersMissing2FA)
	return out, err
}

// SetSecurity mengubah kebijakan. Menyalakan kewajiban ditolak bila admin yang meminta sendiri belum memakai 2FA
// (mencegah mengunci diri sendiri); pengguna lain yang belum punya 2FA baru dipaksa pada permintaan berikutnya.
func (s *Service) SetSecurity(ctx context.Context, actor uuid.UUID, level string) (SecuritySettings, error) {
	if level != Require2FAOff && level != Require2FAAdmin && level != Require2FAAll {
		return SecuritySettings{}, Validation(FieldError{"require_2fa", "harus off, admin, atau all"})
	}
	if level != Require2FAOff {
		st, err := s.MFAStatus(ctx, actor)
		if err != nil {
			return SecuritySettings{}, err
		}
		if !st.Enabled {
			return SecuritySettings{}, NewProblem(http.StatusConflict, "security.enroll_first", "Aktifkan 2FA Anda dahulu",
				"admin yang menyalakan kewajiban 2FA harus sudah memakai 2FA, agar tidak terkunci sendiri")
		}
	}
	raw, _ := json.Marshal(map[string]string{"require_2fa": level})
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
	err := s.Pool.QueryRow(ctx, `SELECT id,email,role,tailscale_login,totp_secret IS NOT NULL,disabled,created_at FROM users WHERE id=$1`, id).
		Scan(&u.ID, &u.Email, &u.Role, &u.TailscaleLogin, &u.MFAEnabled, &u.Disabled, &u.CreatedAt)
	return u, change, err
}

// AdminSetPassword: admin menetapkan password baru untuk pengguna lain (lupa password). Semua sesi targetnya dicabut
// dan penghitung kunci akun direset. Untuk diri sendiri gunakan ChangePassword.
func (s *Service) AdminSetPassword(ctx context.Context, actor, id uuid.UUID, password string) error {
	if id == actor {
		return NewProblem(http.StatusConflict, "user.self_change", "Gunakan Ganti password", "ganti password sendiri lewat /auth/password")
	}
	if len(password) < 12 {
		return Validation(FieldError{"new_password", "minimal 12 karakter"})
	}
	ct, err := s.Pool.Exec(ctx, `UPDATE users SET password_hash=$2, password_changed_at=now(), failed_logins=0, locked_until=NULL WHERE id=$1`, id, HashPassword(password))
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return NotFound("pengguna")
	}
	_, err = s.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id)
	return err
}
