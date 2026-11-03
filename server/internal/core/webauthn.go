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
	errPasskeyOff = NewProblem(http.StatusConflict, "passkey.disabled", "Login passkey dinonaktifkan", "admin belum mengizinkan login tanpa password dengan passkey (Pengaturan > Keamanan)")
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
	Passkey         bool       `json:"passkey"`   // dapat dipakai untuk login tanpa password
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
	rows, err := s.Pool.Query(ctx, `SELECT id, name, created_at, last_used_at, data, passkey FROM webauthn_credentials WHERE user_id=$1 ORDER BY created_at`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebAuthnCredential{}
	for rows.Next() {
		var c WebAuthnCredential
		var raw []byte
		if err := rows.Scan(&c.ID, &c.Name, &c.CreatedAt, &c.LastUsedAt, &raw, &c.Passkey); err != nil {
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
func (s *Service) storeCeremony(ctx context.Context, uid *uuid.UUID, kind, name string, sd *webauthn.SessionData) (uuid.UUID, error) {
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
// uid nil = upacara tanpa pengguna (login passkey). kinds: jenis yang diterima; yang cocok dikembalikan.
func (s *Service) takeCeremony(ctx context.Context, id uuid.UUID, uid *uuid.UUID, kinds ...string) (sd webauthn.SessionData, name, kind string, err error) {
	var raw []byte
	var n *string
	e := s.Pool.QueryRow(ctx, `DELETE FROM webauthn_ceremonies WHERE id=$1 AND user_id IS NOT DISTINCT FROM $2::uuid AND kind = ANY($3) AND expires_at > now() RETURNING data, name, kind`,
		id, uid, kinds).Scan(&raw, &n, &kind)
	if e != nil || json.Unmarshal(raw, &sd) != nil {
		return sd, "", "", errWACeremony
	}
	if n != nil {
		name = *n
	}
	return sd, name, kind, nil
}

type WebAuthnBegin struct {
	CeremonyID uuid.UUID `json:"ceremony_id"`
	Options    any       `json:"options"`
}

// WebAuthnRegisterBegin: butuh password (seperti menyiapkan TOTP). Kunci yang sudah terdaftar dikecualikan agar tidak ganda.
func (s *Service) WebAuthnRegisterBegin(ctx context.Context, uid uuid.UUID, email, password, name string, passkey bool) (WebAuthnBegin, error) {
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
	excl := u.creds
	opts := []webauthn.RegistrationOption{}
	kind := "register"
	if passkey {
		if !s.PasskeyLoginAllowed(ctx) {
			return WebAuthnBegin{}, errPasskeyOff
		}
		// passkey = kredensial discoverable (resident) dengan verifikasi pengguna WAJIB; hanya kunci yang sudah passkey dikecualikan
		excl = nil
		for _, c := range u.creds {
			if s.isPasskeyCred(ctx, uid, c.ID) {
				excl = append(excl, c)
			}
		}
		rk := true
		opts = append(opts, webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: &rk, UserVerification: protocol.VerificationRequired,
		}), webauthn.WithExtensions(protocol.AuthenticationExtensions{"credProps": true}))
		kind = "register_passkey"
	}
	opts = append(opts, webauthn.WithExclusions(webauthn.Credentials(excl).CredentialDescriptors()))
	creation, sd, err := wa.BeginRegistration(u, opts...)
	if err != nil {
		return WebAuthnBegin{}, err
	}
	cid, err := s.storeCeremony(ctx, &uid, kind, name, sd)
	return WebAuthnBegin{CeremonyID: cid, Options: creation}, err
}

// WebAuthnRegisterFinish memverifikasi attestation dan menyimpan kredensial. Bila ini faktor 2FA pertama, kode pemulihan
// diterbitkan (tampil sekali); bila sudah ada faktor lain, kode yang ada tetap dipakai.
func (s *Service) WebAuthnRegisterFinish(ctx context.Context, uid uuid.UUID, email string, ceremony uuid.UUID, credential json.RawMessage) (WebAuthnCredential, []string, error) {
	wa, err := s.webAuthn()
	if err != nil {
		return WebAuthnCredential{}, nil, errWADisabled
	}
	sd, name, kind, err := s.takeCeremony(ctx, ceremony, &uid, "register", "register_passkey")
	if err != nil {
		return WebAuthnCredential{}, nil, err
	}
	passkey := kind == "register_passkey"
	if passkey && !s.PasskeyLoginAllowed(ctx) {
		return WebAuthnCredential{}, nil, errPasskeyOff
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
	if passkey {
		// verifikasi pengguna (PIN/biometrik) wajib agar passkey sah menggantikan password + faktor kedua
		if !cred.Flags.UserVerified {
			return WebAuthnCredential{}, nil, Validation(FieldError{"credential", "authenticator tidak melakukan verifikasi pengguna (PIN/biometrik); tidak dapat dipakai sebagai passkey"})
		}
		var ext struct {
			Ext struct {
				CredProps struct {
					RK *bool `json:"rk"`
				} `json:"credProps"`
			} `json:"clientExtensionResults"`
		}
		if json.Unmarshal(credential, &ext) == nil && ext.Ext.CredProps.RK != nil && !*ext.Ext.CredProps.RK {
			return WebAuthnCredential{}, nil, Validation(FieldError{"credential", "authenticator tidak membuat kredensial yang dapat ditemukan (resident key); gunakan sebagai kunci keamanan biasa"})
		}
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
	out := WebAuthnCredential{ID: newID(), Name: name, CreatedAt: time.Now(), BackedUp: cred.Flags.BackupState, Transports: []string{}, Passkey: passkey}
	for _, t := range cred.Transport {
		out.Transports = append(out.Transports, string(t))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO webauthn_credentials(id,user_id,credential_id,name,data,passkey) VALUES ($1,$2,$3,$4,$5,$6)`, out.ID, uid, cred.ID, name, raw, passkey); err != nil {
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
	cid, err := s.storeCeremony(ctx, &cu.ID, "login", "", sd)
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
	sd, _, _, err := s.takeCeremony(ctx, ceremony, &cu.ID, "login")
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

func (s *Service) isPasskeyCred(ctx context.Context, uid uuid.UUID, credID []byte) bool {
	var pk bool
	_ = s.Pool.QueryRow(ctx, `SELECT passkey FROM webauthn_credentials WHERE user_id=$1 AND credential_id=$2`, uid, credID).Scan(&pk)
	return pk
}

// PasskeyLoginBegin: login tanpa password — pengguna belum diketahui (kredensial discoverable). Verifikasi pengguna WAJIB.
func (s *Service) PasskeyLoginBegin(ctx context.Context) (WebAuthnBegin, error) {
	wa, err := s.webAuthn()
	if err != nil {
		return WebAuthnBegin{}, errWADisabled
	}
	if !s.PasskeyLoginAllowed(ctx) {
		return WebAuthnBegin{}, errPasskeyOff
	}
	assertion, sd, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return WebAuthnBegin{}, err
	}
	cid, err := s.storeCeremony(ctx, nil, "passkey_login", "", sd)
	return WebAuthnBegin{CeremonyID: cid, Options: assertion}, err
}

// PasskeyLoginFinish memverifikasi assertion discoverable. Passkey = kepemilikan perangkat + verifikasi pengguna, jadi TIDAK ada
// langkah kedua. Syarat: kredensial bertanda passkey, akun aktif & tidak terkunci, UV benar-benar terjadi, sign count tidak mundur.
func (s *Service) PasskeyLoginFinish(ctx context.Context, ceremony uuid.UUID, credential json.RawMessage) (LoginResult, error) {
	wa, err := s.webAuthn()
	if err != nil {
		return LoginResult{}, errWADisabled
	}
	if !s.PasskeyLoginAllowed(ctx) {
		return LoginResult{}, errPasskeyOff
	}
	sd, _, _, err := s.takeCeremony(ctx, ceremony, nil, "passkey_login")
	if err != nil {
		return LoginResult{}, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credential)
	if err != nil {
		return LoginResult{}, errWAFailed
	}
	var found User
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		uid, e := uuid.FromBytes(userHandle)
		if e != nil {
			return nil, errors.New("user handle tidak valid")
		}
		var locked *time.Time
		var disabled, isPasskey bool
		if e := s.Pool.QueryRow(ctx, `SELECT u.id,u.email,u.role,u.tailscale_login,`+mfaAny("u")+`,u.locked_until,u.disabled,
			EXISTS (SELECT 1 FROM webauthn_credentials w WHERE w.user_id=u.id AND w.credential_id=$2 AND w.passkey)
			FROM users u WHERE u.id=$1`, uid, rawID).Scan(&found.ID, &found.Email, &found.Role, &found.TailscaleLogin, &found.MFAEnabled, &locked, &disabled, &isPasskey); e != nil {
			return nil, errors.New("pengguna tidak ditemukan")
		}
		if disabled || !isPasskey {
			return nil, errors.New("akun nonaktif atau kredensial bukan passkey")
		}
		if locked != nil && locked.After(time.Now()) {
			return nil, errLocked
		}
		return s.loadWAUser(ctx, uid, found.Email)
	}
	_, cred, err := wa.ValidatePasskeyLogin(handler, sd, parsed)
	if err != nil {
		if errors.Is(err, errLocked) {
			return LoginResult{}, NewProblem(http.StatusLocked, "auth.locked", "Akun terkunci", "terlalu banyak percobaan, coba lagi nanti")
		}
		return LoginResult{}, errWAFailed
	}
	if cred.Authenticator.CloneWarning {
		s.Audit(ctx, "user:"+found.Email, "auth.webauthn_clone_warning", "user", found.ID.String(), nil)
		return LoginResult{}, errWAFailed
	}
	raw, _ := json.Marshal(cred)
	if _, err := s.Pool.Exec(ctx, `UPDATE webauthn_credentials SET data=$3, last_used_at=now() WHERE user_id=$1 AND credential_id=$2`, found.ID, cred.ID, raw); err != nil {
		return LoginResult{}, err
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE users SET failed_logins=0, locked_until=NULL WHERE id=$1`, found.ID)
	return s.startSession(ctx, found)
}

var errLocked = errors.New("akun terkunci")
