package core

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// WebAuthn (fase 9): kunci keamanan / passkey sebagai FAKTOR KEDUA (bukan login tanpa password). Verifikasi kriptografis
// (attestation "none", tanda tangan, origin, RP ID hash, challenge, sign count) dilakukan pustaka go-webauthn; kita
// menyediakan penyimpanan kredensial, upacara sekali pakai, batas laju/lockout, dan integrasi dengan kebijakan 2FA.

const (
	waCeremonyTTL    = 5 * time.Minute
	waMaxCredentials = 10
)

var (
	errWACeremony = NewProblem(http.StatusBadRequest, "webauthn.ceremony_invalid", "Upacara tidak valid", "sesi WebAuthn tidak ditemukan, sudah dipakai, atau kedaluwarsa; ulangi dari awal")
	errWAFailed   = NewProblem(http.StatusUnauthorized, "auth.webauthn_failed", "Verifikasi kunci keamanan gagal", "kunci keamanan tidak dikenali atau respons tidak valid")
	errWADisabled = NewProblem(http.StatusConflict, "webauthn.not_available", "WebAuthn tidak tersedia", "RP ID server tidak valid (alamat IP?); atur WEBAUTHN_RP_ID dan PUBLIC_BASE_URL dengan nama domain")
)

type waState struct {
	once sync.Once
	wa   *webauthn.WebAuthn
	err  error
}

// WebAuthnEnabled: RP ID harus nama domain (browser menolak alamat IP) dan konfigurasi pustaka valid.
func (s *Service) WebAuthnEnabled() bool {
	_, err := s.webAuthn()
	return err == nil
}

func (s *Service) webAuthn() (*webauthn.WebAuthn, error) {
	s.waSt.once.Do(func() {
		id := s.Cfg.WebAuthnRPID
		if id == "" || net.ParseIP(strings.Trim(id, "[]")) != nil {
			s.waSt.err = errWADisabled
			return
		}
		s.waSt.wa, s.waSt.err = webauthn.New(&webauthn.Config{
			RPID: id, RPDisplayName: s.Cfg.WebAuthnRPName, RPOrigins: s.Cfg.WebAuthnOrigins,
			AttestationPreference: protocol.PreferNoAttestation,
			AuthenticatorSelection: protocol.AuthenticatorSelection{
				ResidentKey:      protocol.ResidentKeyRequirementDiscouraged, // faktor kedua: tidak perlu passkey yang dapat ditemukan
				UserVerification: protocol.VerificationPreferred,
			},
			Timeouts: webauthn.TimeoutsConfig{
				Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: waCeremonyTTL},
				Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: waCeremonyTTL},
			},
		})
	})
	return s.waSt.wa, s.waSt.err
}

type waUser struct {
	id    uuid.UUID
	email string
	creds []webauthn.Credential
}

func (u waUser) WebAuthnID() []byte                         { return u.id[:] } // 16 byte UUID: opak, bukan data pribadi
func (u waUser) WebAuthnName() string                       { return u.email }
func (u waUser) WebAuthnDisplayName() string                { return u.email }
func (u waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

type WebAuthnCredential struct {
	ID              uuid.UUID  `json:"id"`
	Name            string     `json:"name"`
	CreatedAt       time.Time  `json:"created_at"`
	LastUsedAt      *time.Time `json:"last_used_at"`
	BackedUp        bool       `json:"backed_up"` // passkey tersinkron (BS), mis. iCloud / Google
	Transports      []string   `json:"transports"`
	rawCredentialID []byte
}

func (s *Service) loadWAUser(ctx context.Context, uid uuid.UUID, email string) (waUser, error) {
	u := waUser{id: uid, email: email}
	rows, err := s.Pool.Query(ctx, `SELECT data FROM webauthn_credentials WHERE user_id=$1 ORDER BY created_at`, uid)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var c webauthn.Credential
		if rows.Scan(&raw) == nil && json.Unmarshal(raw, &c) == nil {
			u.creds = append(u.creds, c)
		}
	}
	return u, rows.Err()
}

func (s *Service) ListWebAuthn(ctx context.Context, uid uuid.UUID) ([]WebAuthnCredential, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, name, created_at, last_used_at, data FROM webauthn_credentials WHERE user_id=$1 ORDER BY created_at`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebAuthnCredential{}
	for rows.Next() {
		var c WebAuthnCredential
		var raw []byte
		if err := rows.Scan(&c.ID, &c.Name, &c.CreatedAt, &c.LastUsedAt, &raw); err != nil {
			return nil, err
		}
		var cred webauthn.Credential
		_ = json.Unmarshal(raw, &cred)
		c.BackedUp = cred.Flags.BackupState
		c.Transports = []string{}
		for _, t := range cred.Transport {
			c.Transports = append(c.Transports, string(t))
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// storeCeremony menyimpan state upacara (challenge dll.); sekali pakai dan berumur 5 menit.
func (s *Service) storeCeremony(ctx context.Context, uid uuid.UUID, kind, name string, sd *webauthn.SessionData) (uuid.UUID, error) {
	_, _ = s.Pool.Exec(ctx, `DELETE FROM webauthn_ceremonies WHERE expires_at < now()`)
	raw, err := json.Marshal(sd)
	if err != nil {
		return uuid.Nil, err
	}
	id := newID()
	var n *string
	if name != "" {
		n = &name
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO webauthn_ceremonies(id,user_id,kind,data,name,expires_at) VALUES ($1,$2,$3,$4,$5, now() + make_interval(secs => $6))`,
		id, uid, kind, raw, n, waCeremonyTTL.Seconds())
	return id, err
}

// takeCeremony mengambil DAN menghapus (atomik) — satu upacara hanya bisa diselesaikan sekali, juga bila gagal.
func (s *Service) takeCeremony(ctx context.Context, id, uid uuid.UUID, kind string) (webauthn.SessionData, string, error) {
	var raw []byte
	var name *string
	err := s.Pool.QueryRow(ctx, `DELETE FROM webauthn_ceremonies WHERE id=$1 AND user_id=$2 AND kind=$3 AND expires_at > now() RETURNING data, name`, id, uid, kind).Scan(&raw, &name)
	if err != nil {
		return webauthn.SessionData{}, "", errWACeremony
	}
	var sd webauthn.SessionData
	if json.Unmarshal(raw, &sd) != nil {
		return sd, "", errWACeremony
	}
	if name == nil {
		return sd, "", nil
	}
	return sd, *name, nil
}

type WebAuthnBegin struct {
	CeremonyID uuid.UUID `json:"ceremony_id"`
	Options    any       `json:"options"`
}

// WebAuthnRegisterBegin: butuh password (seperti menyiapkan TOTP). Kunci yang sudah terdaftar dikecualikan agar tidak ganda.
func (s *Service) WebAuthnRegisterBegin(ctx context.Context, uid uuid.UUID, email, password, name string) (WebAuthnBegin, error) {
	wa, err := s.webAuthn()
	if err != nil {
		return WebAuthnBegin{}, errWADisabled
	}
	if err := s.verifyPassword(ctx, uid, password); err != nil {
		return WebAuthnBegin{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Kunci keamanan"
	}
	if len([]rune(name)) > 40 {
		return WebAuthnBegin{}, Validation(FieldError{"name", "maksimal 40 karakter"})
	}
	u, err := s.loadWAUser(ctx, uid, email)
	if err != nil {
		return WebAuthnBegin{}, err
	}
	if len(u.creds) >= waMaxCredentials {
		return WebAuthnBegin{}, NewProblem(http.StatusConflict, "webauthn.limit", "Terlalu banyak kunci", "maksimal 10 kunci keamanan per akun; hapus yang tidak dipakai")
	}
	creation, sd, err := wa.BeginRegistration(u, webauthn.WithExclusions(webauthn.Credentials(u.creds).CredentialDescriptors()))
	if err != nil {
		return WebAuthnBegin{}, err
	}
	cid, err := s.storeCeremony(ctx, uid, "register", name, sd)
	return WebAuthnBegin{CeremonyID: cid, Options: creation}, err
}

// WebAuthnRegisterFinish memverifikasi attestation dan menyimpan kredensial. Bila ini faktor 2FA pertama, kode pemulihan
// diterbitkan (tampil sekali); bila sudah ada faktor lain, kode yang ada tetap dipakai.
func (s *Service) WebAuthnRegisterFinish(ctx context.Context, uid uuid.UUID, email string, ceremony uuid.UUID, credential json.RawMessage) (WebAuthnCredential, []string, error) {
	wa, err := s.webAuthn()
	if err != nil {
		return WebAuthnCredential{}, nil, errWADisabled
	}
	sd, name, err := s.takeCeremony(ctx, ceremony, uid, "register")
	if err != nil {
		return WebAuthnCredential{}, nil, err
	}
	u, err := s.loadWAUser(ctx, uid, email)
	if err != nil {
		return WebAuthnCredential{}, nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(credential)
	if err != nil {
		return WebAuthnCredential{}, nil, Validation(FieldError{"credential", "respons pendaftaran tidak valid: " + shortErr(err)})
	}
	cred, err := wa.CreateCredential(u, sd, parsed)
	if err != nil {
		return WebAuthnCredential{}, nil, Validation(FieldError{"credential", "verifikasi pendaftaran gagal: " + shortErr(err)})
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return WebAuthnCredential{}, nil, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return WebAuthnCredential{}, nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var hadMFA bool
	if err := tx.QueryRow(ctx, `SELECT `+mfaAny("users")+` FROM users WHERE id=$1 FOR UPDATE`, uid).Scan(&hadMFA); err != nil {
		return WebAuthnCredential{}, nil, err
	}
	out := WebAuthnCredential{ID: newID(), Name: name, CreatedAt: time.Now(), BackedUp: cred.Flags.BackupState, Transports: []string{}}
	for _, t := range cred.Transport {
		out.Transports = append(out.Transports, string(t))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO webauthn_credentials(id,user_id,credential_id,name,data) VALUES ($1,$2,$3,$4,$5)`, out.ID, uid, cred.ID, name, raw); err != nil {
		if isUnique(err, "") {
			return WebAuthnCredential{}, nil, NewProblem(http.StatusConflict, "resource.conflict", "Konflik", "kunci keamanan ini sudah terdaftar")
		}
		return WebAuthnCredential{}, nil, err
	}
	codes := []string{}
	if !hadMFA {
		var hashes [][]byte
		codes, hashes = newRecoverySet(uid)
		if err := replaceRecovery(ctx, tx, uid, hashes); err != nil {
			return WebAuthnCredential{}, nil, err
		}
	}
	return out, codes, tx.Commit(ctx)
}

// WebAuthnRemove menghapus satu kunci; butuh password + kode (TOTP/pemulihan), sama ketatnya dengan menonaktifkan 2FA.
// Bila itu faktor terakhir (tanpa TOTP), kode pemulihan ikut dibuang karena 2FA praktis mati.
func (s *Service) WebAuthnRemove(ctx context.Context, uid, id uuid.UUID, password, code string) error {
	var one int // cek kepemilikan DULU agar kode pemulihan tidak terpakai sia-sia pada permintaan yang pasti 404
	if s.Pool.QueryRow(ctx, `SELECT 1 FROM webauthn_credentials WHERE id=$1 AND user_id=$2`, id, uid).Scan(&one) != nil {
		return NotFound("kunci keamanan")
	}
	if err := s.reauth(ctx, uid, password, code); err != nil {
		return err
	}
	ct, err := s.Pool.Exec(ctx, `DELETE FROM webauthn_credentials WHERE id=$1 AND user_id=$2`, id, uid)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return NotFound("kunci keamanan")
	}
	st, err := s.MFAStatus(ctx, uid)
	if err == nil && !st.Enabled {
		_, _ = s.Pool.Exec(ctx, `DELETE FROM user_recovery_codes WHERE user_id=$1`, uid)
	}
	return nil
}

// mfaMethods: metode yang tersedia untuk tantangan login (urutan tampilan di UI).
func (s *Service) mfaMethods(ctx context.Context, uid uuid.UUID) []string {
	var totp bool
	var n int
	_ = s.Pool.QueryRow(ctx, `SELECT totp_secret IS NOT NULL, (SELECT count(*) FROM webauthn_credentials w WHERE w.user_id=users.id) FROM users WHERE id=$1`, uid).Scan(&totp, &n)
	out := []string{}
	if n > 0 && s.WebAuthnEnabled() {
		out = append(out, "webauthn")
	}
	if totp {
		out = append(out, "totp")
	}
	return append(out, "recovery")
}

// WebAuthnLoginBegin: untuk tantangan login (mfa_token). Hanya kredensial milik pengguna itu yang diizinkan.
func (s *Service) WebAuthnLoginBegin(ctx context.Context, mfaToken string) (WebAuthnBegin, error) {
	wa, err := s.webAuthn()
	if err != nil {
		return WebAuthnBegin{}, errWADisabled
	}
	cu, err := s.challengeUser(ctx, mfaToken)
	if err != nil {
		return WebAuthnBegin{}, err
	}
	u, err := s.loadWAUser(ctx, cu.ID, cu.Email)
	if err != nil {
		return WebAuthnBegin{}, err
	}
	if len(u.creds) == 0 {
		return WebAuthnBegin{}, NewProblem(http.StatusConflict, "webauthn.no_credentials", "Tidak ada kunci keamanan", "akun ini tidak punya kunci keamanan; gunakan kode TOTP atau kode pemulihan")
	}
	assertion, sd, err := wa.BeginLogin(u)
	if err != nil {
		return WebAuthnBegin{}, err
	}
	cid, err := s.storeCeremony(ctx, cu.ID, "login", "", sd)
	return WebAuthnBegin{CeremonyID: cid, Options: assertion}, err
}

// WebAuthnLoginFinish memverifikasi assertion. Kegagalan dihitung seperti salah kode (5x → kunci 15 menit). Sign count yang
// mundur (kemungkinan kunci diklon) ditolak dan diaudit.
func (s *Service) WebAuthnLoginFinish(ctx context.Context, mfaToken string, ceremony uuid.UUID, credential json.RawMessage) (LoginResult, error) {
	wa, err := s.webAuthn()
	if err != nil {
		return LoginResult{}, errWADisabled
	}
	cu, err := s.challengeUser(ctx, mfaToken)
	if err != nil {
		return LoginResult{}, err
	}
	sd, _, err := s.takeCeremony(ctx, ceremony, cu.ID, "login")
	if err != nil {
		return LoginResult{}, err
	}
	fail := func() (LoginResult, error) { s.failSecondFactor(ctx, cu.ID); return LoginResult{}, errWAFailed }
	u, err := s.loadWAUser(ctx, cu.ID, cu.Email)
	if err != nil {
		return LoginResult{}, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credential)
	if err != nil {
		return fail()
	}
	cred, err := wa.ValidateLogin(u, sd, parsed)
	if err != nil {
		return fail()
	}
	if cred.Authenticator.CloneWarning {
		s.Audit(ctx, "user:"+cu.Email, "auth.webauthn_clone_warning", "user", cu.ID.String(), nil)
		return fail()
	}
	raw, _ := json.Marshal(cred)
	if _, err := s.Pool.Exec(ctx, `UPDATE webauthn_credentials SET data=$3, last_used_at=now() WHERE user_id=$1 AND credential_id=$2`, cu.ID, cred.ID, raw); err != nil {
		return LoginResult{}, err
	}
	return s.finishSecondFactor(ctx, cu, "webauthn")
}

func shortErr(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return truncate(pe.Details, 160)
	}
	return truncate(err.Error(), 160)
}

var _ = pgx.ErrNoRows
