package core

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

// SSO dengan OpenID Connect (fase 11): authorization code + PKCE (S256) + nonce + state, dengan "binder" cookie yang mengikat
// alur ke peramban yang memulainya (mencegah login-CSRF / fiksasi sesi). Token ID diverifikasi penuh oleh go-oidc (tanda tangan
// dari JWKS penerbit, iss, aud, exp, algoritma dibatasi). Identitas = (issuer, sub), BUKAN email.

const (
	oidcFlowTTL    = 10 * time.Minute
	oidcBinderName = "smsc_oidc"
)

// OIDCError membawa kode ringkas untuk UI (?sso_error=kode) dan detail untuk log/audit; detail tidak dikirim ke peramban.
type OIDCError struct {
	Code   string
	Detail string
}

func (e *OIDCError) Error() string { return e.Code + ": " + e.Detail }

func oerr(code, f string, a ...any) *OIDCError {
	return &OIDCError{Code: code, Detail: fmt.Sprintf(f, a...)}
}

var errSSOOff = NewProblem(http.StatusNotFound, "sso.not_configured", "SSO belum dikonfigurasi", "atur OIDC_ISSUER dan OIDC_CLIENT_ID di server lalu restart")

type oidcState struct {
	mu       sync.Mutex
	provider *oidc.Provider
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
	failedAt time.Time
}

func (s *Service) OIDCEnabled() bool { return s.Cfg.OIDC.Enabled() }

func (s *Service) oidcCtx(ctx context.Context) context.Context {
	if c := s.Cfg.OIDC.HTTPClient; c != nil {
		return oidc.ClientContext(ctx, c)
	}
	return ctx
}

// oidcInit melakukan discovery sekali (di-cache); kegagalan tidak di-cache lebih dari 10 detik agar IdP yang pulih terpakai lagi.
func (s *Service) oidcInit(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	st := &s.oidcSt
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.provider != nil {
		return st.oauth, st.verifier, nil
	}
	if time.Since(st.failedAt) < 10*time.Second && !st.failedAt.IsZero() {
		return nil, nil, oerr("unavailable", "discovery gagal baru-baru ini")
	}
	c := s.Cfg.OIDC
	dctx, cancel := context.WithTimeout(s.oidcCtx(ctx), 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(dctx, c.Issuer)
	if err != nil {
		st.failedAt = time.Now()
		return nil, nil, oerr("unavailable", "discovery: %v", err)
	}
	st.provider = p
	st.oauth = &oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, Endpoint: p.Endpoint(), RedirectURL: c.RedirectURL, Scopes: c.Scopes}
	st.verifier = p.Verifier(&oidc.Config{ClientID: c.ClientID, SupportedSigningAlgs: []string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512", "PS256", "PS384", "PS512", "EdDSA"}})
	return st.oauth, st.verifier, nil
}

// SafeNext: hanya path lokal ("/..."); menolak //host, /\host, skema, dan karakter kontrol (open redirect).
func SafeNext(next string) string {
	if next == "" || len(next) > 300 || next[0] != '/' || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n\t") {
		return "/"
	}
	if u, err := url.Parse(next); err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return next
}

// OIDCBegin memulai alur. purpose "login" (userID nil) atau "link" (userID = pemilik sesi). Mengembalikan URL IdP dan nilai binder cookie.
func (s *Service) OIDCBegin(ctx context.Context, purpose string, userID *uuid.UUID, next string) (authURL, binder string, err error) {
	if !s.OIDCEnabled() {
		return "", "", errSSOOff
	}
	cfg, _, err := s.oidcInit(ctx)
	if err != nil {
		return "", "", err
	}
	_, _ = s.Pool.Exec(ctx, `DELETE FROM oidc_flows WHERE expires_at < now()`)
	state, binder, nonce, verifier := randToken(32), randToken(32), randToken(24), oauth2.GenerateVerifier()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO oidc_flows(state_hash,binder_hash,nonce,verifier,purpose,user_id,next_path,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7, now() + make_interval(secs => $8))`,
		sha(state), sha(binder), nonce, verifier, purpose, userID, SafeNext(next), oidcFlowTTL.Seconds()); err != nil {
		return "", "", err
	}
	return cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)), binder, nil
}

type oidcClaims struct {
	Subject       string          `json:"sub"`
	Email         string          `json:"email"`
	EmailVerified json.RawMessage `json:"email_verified"`
	Name          string          `json:"name"`
	Raw           map[string]json.RawMessage
}

func (c oidcClaims) emailVerified() bool {
	var b bool
	if json.Unmarshal(c.EmailVerified, &b) == nil {
		return b
	}
	var str string // beberapa IdP mengirim "true" sebagai string
	return json.Unmarshal(c.EmailVerified, &str) == nil && strings.EqualFold(str, "true")
}

func (c oidcClaims) groups(claim string) []string {
	raw, ok := c.Raw[claim]
	if !ok {
		return nil
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return splitCSV(one)
	}
	return nil
}

// OIDCResult: hasil callback. Untuk login berisi sesi; untuk link hanya Linked=true.
type OIDCResult struct {
	Purpose string
	Login   LoginResult
	Next    string
	UserID  uuid.UUID
	Email   string
	Created bool // pengguna dibuat (penyediaan otomatis)
	Linked  bool
}

// OIDCCallback memproses redirect dari IdP. sessionUser = pengguna dari cookie sesi (untuk purpose link), idpErr = parameter `error`.
func (s *Service) OIDCCallback(ctx context.Context, state, code, binder, idpErr string, sessionUser *uuid.UUID) (OIDCResult, error) {
	var res OIDCResult
	if !s.OIDCEnabled() {
		return res, errSSOOff
	}
	if state == "" {
		return res, oerr("invalid_state", "state kosong")
	}
	var nonce, verifier, purpose string
	var binderHash []byte
	var flowUser *uuid.UUID
	var next *string
	err := s.Pool.QueryRow(ctx, `DELETE FROM oidc_flows WHERE state_hash=$1 AND expires_at > now() RETURNING binder_hash, nonce, verifier, purpose, user_id, next_path`, sha(state)).
		Scan(&binderHash, &nonce, &verifier, &purpose, &flowUser, &next)
	if err != nil {
		return res, oerr("invalid_state", "state tidak dikenal, kedaluwarsa, atau sudah dipakai")
	}
	res.Purpose = purpose
	if next != nil {
		res.Next = *next
	}
	if binder == "" || subtle.ConstantTimeCompare(sha(binder), binderHash) != 1 {
		return res, oerr("invalid_state", "cookie pengikat tidak cocok (alur dimulai di peramban lain?)")
	}
	if purpose == "link" && (flowUser == nil || sessionUser == nil || *flowUser != *sessionUser) {
		return res, oerr("session_mismatch", "sesi saat callback bukan pemilik alur penautan")
	}
	if idpErr != "" {
		return res, oerr("denied", "IdP mengembalikan error=%s", truncate(idpErr, 60))
	}
	if code == "" {
		return res, oerr("invalid_state", "code kosong")
	}
	cfg, vf, err := s.oidcInit(ctx)
	if err != nil {
		return res, err
	}
	xctx, cancel := context.WithTimeout(s.oidcCtx(ctx), 15*time.Second)
	defer cancel()
	tok, err := cfg.Exchange(xctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return res, oerr("token", "penukaran code gagal: %v", err)
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return res, oerr("token", "respons token tanpa id_token")
	}
	idt, err := vf.Verify(xctx, rawID)
	if err != nil {
		return res, oerr("token", "verifikasi id_token gagal: %v", err)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(nonce)) != 1 {
		return res, oerr("token", "nonce tidak cocok")
	}
	var cl oidcClaims
	if err := idt.Claims(&cl); err != nil || cl.Subject == "" {
		return res, oerr("token", "klaim tidak valid")
	}
	_ = idt.Claims(&cl.Raw)
	issuer := idt.Issuer
	if purpose == "link" {
		return s.oidcLink(ctx, res, issuer, cl, *flowUser)
	}
	return s.oidcLogin(ctx, res, issuer, cl)
}

func (s *Service) oidcRole(cl oidcClaims) string {
	c := s.Cfg.OIDC
	role := c.DefaultRole
	groups := map[string]bool{}
	for _, g := range cl.groups(c.GroupsClaim) {
		groups[g] = true
	}
	for _, g := range c.OperatorGroups {
		if groups[g] {
			role = "operator"
		}
	}
	for _, g := range c.AdminGroups {
		if groups[g] {
			return "admin"
		}
	}
	return role
}

func (s *Service) domainAllowed(email string) bool {
	if len(s.Cfg.OIDC.AllowedDomains) == 0 {
		return true
	}
	i := strings.LastIndexByte(email, '@')
	if i < 0 {
		return false
	}
	d := strings.ToLower(email[i+1:])
	for _, a := range s.Cfg.OIDC.AllowedDomains {
		if d == a {
			return true
		}
	}
	return false
}

func (s *Service) oidcLogin(ctx context.Context, res OIDCResult, issuer string, cl oidcClaims) (OIDCResult, error) {
	var u User
	var disabled bool
	err := s.Pool.QueryRow(ctx, `SELECT id,email,role,tailscale_login,`+mfaAny("users")+`,password_hash IS NOT NULL,disabled FROM users WHERE oidc_issuer=$1 AND oidc_subject=$2`, issuer, cl.Subject).
		Scan(&u.ID, &u.Email, &u.Role, &u.TailscaleLogin, &u.MFAEnabled, &u.HasPassword, &disabled)
	switch {
	case err == nil:
		if disabled {
			return res, oerr("disabled", "akun %s nonaktif", u.Email)
		}
		if s.Cfg.OIDC.SyncRoles && len(s.Cfg.OIDC.AdminGroups)+len(s.Cfg.OIDC.OperatorGroups) > 0 {
			if r := s.oidcRole(cl); r != u.Role {
				if _, err := s.Pool.Exec(ctx, `UPDATE users SET role=$2 WHERE id=$1`, u.ID, r); err != nil {
					return res, err
				}
				s.Audit(ctx, "oidc:"+u.Email, "user.update", "user", u.ID.String(), map[string]any{"role": map[string]string{"from": u.Role, "to": r}, "via": "oidc_sync"})
				u.Role = r
			}
		}
	case errors.Is(err, pgx.ErrNoRows):
		email := strings.ToLower(strings.TrimSpace(cl.Email))
		if email == "" || !cl.emailVerified() {
			return res, oerr("unverified_email", "IdP tidak memberi email terverifikasi untuk sub=%s", cl.Subject)
		}
		if !s.domainAllowed(email) {
			return res, oerr("domain", "domain email %s tidak diizinkan", email[strings.LastIndexByte(email, '@')+1:])
		}
		var existing uuid.UUID
		var existingSub *string
		var existingDisabled bool
		e2 := s.Pool.QueryRow(ctx, `SELECT id, oidc_subject, disabled FROM users WHERE lower(email)=$1`, email).Scan(&existing, &existingSub, &existingDisabled)
		linkedNow := false
		switch {
		case e2 == nil:
			// email sudah milik pengguna lokal: tautkan hanya bila diizinkan eksplisit DAN belum tertaut ke identitas IdP lain
			if !s.Cfg.OIDC.LinkByEmail || existingSub != nil {
				return res, oerr("not_linked", "email %s sudah terdaftar; tautkan dari halaman Akun atau aktifkan OIDC_LINK_BY_EMAIL", email)
			}
			if existingDisabled {
				return res, oerr("disabled", "akun %s nonaktif", email)
			}
			ct, err := s.Pool.Exec(ctx, `UPDATE users SET oidc_issuer=$2, oidc_subject=$3, oidc_linked_at=now() WHERE id=$1 AND oidc_subject IS NULL`, existing, issuer, cl.Subject)
			if err != nil || ct.RowsAffected() != 1 {
				return res, oerr("conflict", "gagal menautkan (sudah tertaut oleh proses lain?): %v", err)
			}
			s.Audit(ctx, "oidc:"+email, "auth.oidc_link", "user", existing.String(), map[string]any{"via": "email", "issuer": issuer})
			// muat ulang SEKALI (tanpa rekursi) sebagai pengguna tertaut
			if err := s.Pool.QueryRow(ctx, `SELECT id,email,role,tailscale_login,`+mfaAny("users")+`,password_hash IS NOT NULL FROM users WHERE id=$1`, existing).
				Scan(&u.ID, &u.Email, &u.Role, &u.TailscaleLogin, &u.MFAEnabled, &u.HasPassword); err != nil {
				return res, err
			}
			linkedNow = true
		case !errors.Is(e2, pgx.ErrNoRows):
			return res, e2
		}
		if linkedNow {
			break
		}
		if !s.Cfg.OIDC.AutoProvision {
			return res, oerr("not_linked", "belum ada akun yang tertaut untuk sub=%s (penyediaan otomatis mati)", cl.Subject)
		}
		u = User{ID: newID(), Email: email, Role: s.oidcRole(cl)}
		if _, err := s.Pool.Exec(ctx, `INSERT INTO users(id,email,password_hash,role,oidc_issuer,oidc_subject,oidc_linked_at) VALUES ($1,$2,NULL,$3,$4,$5,now())`, u.ID, email, u.Role, issuer, cl.Subject); err != nil {
			if isUnique(err, "") {
				return res, oerr("conflict", "email atau identitas sudah ada")
			}
			return res, err
		}
		res.Created = true
		s.Audit(ctx, "oidc:"+email, "auth.oidc_provision", "user", u.ID.String(), map[string]any{"role": u.Role, "issuer": issuer})
	default:
		return res, err
	}
	u.SSOLinked = true
	lr, err := s.startSessionSSO(ctx, u, true)
	if err != nil {
		return res, err
	}
	res.Login, res.UserID, res.Email = lr, u.ID, u.Email
	return res, nil
}

func (s *Service) oidcLink(ctx context.Context, res OIDCResult, issuer string, cl oidcClaims, uid uuid.UUID) (OIDCResult, error) {
	var email string
	if err := s.Pool.QueryRow(ctx, `SELECT email FROM users WHERE id=$1 AND NOT disabled`, uid).Scan(&email); err != nil {
		return res, oerr("disabled", "pengguna tidak ditemukan / nonaktif")
	}
	ct, err := s.Pool.Exec(ctx, `UPDATE users SET oidc_issuer=$2, oidc_subject=$3, oidc_linked_at=now() WHERE id=$1 AND oidc_subject IS NULL`, uid, issuer, cl.Subject)
	if err != nil {
		if isUnique(err, "") {
			return res, oerr("link_conflict", "identitas SSO ini sudah tertaut ke akun lain")
		}
		return res, err
	}
	if ct.RowsAffected() == 0 {
		return res, oerr("link_conflict", "akun ini sudah tertaut ke identitas SSO")
	}
	s.Audit(ctx, "user:"+email, "auth.oidc_link", "user", uid.String(), map[string]any{"via": "account", "issuer": issuer})
	res.UserID, res.Email, res.Linked = uid, email, true
	return res, nil
}

// OIDCUnlink: lepas tautan SSO. Ditolak bila akun tidak punya password (akan terkunci permanen).
func (s *Service) OIDCUnlink(ctx context.Context, uid uuid.UUID) error {
	var hasPw, linked bool
	var email string
	if err := s.Pool.QueryRow(ctx, `SELECT password_hash IS NOT NULL, oidc_subject IS NOT NULL, email FROM users WHERE id=$1`, uid).Scan(&hasPw, &linked, &email); err != nil {
		return NotFound("pengguna")
	}
	if !linked {
		return NewProblem(http.StatusConflict, "sso.not_linked", "Tidak tertaut", "akun ini tidak tertaut ke SSO")
	}
	if !hasPw {
		return NewProblem(http.StatusConflict, "sso.unlink_blocked", "Tidak dapat melepas SSO", "akun ini tidak punya password; minta admin menetapkan password dahulu agar tidak terkunci")
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE users SET oidc_issuer=NULL, oidc_subject=NULL, oidc_linked_at=NULL WHERE id=$1`, uid); err != nil {
		return err
	}
	s.Audit(ctx, "user:"+email, "auth.oidc_unlink", "user", uid.String(), nil)
	return nil
}

type SSOView struct {
	Enabled       bool     `json:"enabled"`
	Name          string   `json:"name,omitempty"`
	Issuer        string   `json:"issuer,omitempty"`
	ClientID      string   `json:"client_id,omitempty"`
	RedirectURL   string   `json:"redirect_url,omitempty"`
	Reachable     *bool    `json:"reachable,omitempty"`
	ReachError    string   `json:"reach_error,omitempty"`
	AutoProvision bool     `json:"auto_provision"`
	DefaultRole   string   `json:"default_role,omitempty"`
	LinkByEmail   bool     `json:"link_by_email"`
	SyncRoles     bool     `json:"sync_roles"`
	TrustIdPMFA   bool     `json:"trust_idp_mfa"`
	Domains       []string `json:"allowed_domains"`
	LinkedUsers   int      `json:"linked_users"`
}

// SSOConfig: ringkasan konfigurasi (tanpa rahasia) + uji discovery bila diminta.
func (s *Service) SSOConfig(ctx context.Context, probe bool) SSOView {
	c := s.Cfg.OIDC
	v := SSOView{Enabled: c.Enabled(), Domains: []string{}}
	if !c.Enabled() {
		return v
	}
	v.Name, v.Issuer, v.ClientID, v.RedirectURL = c.Name, c.Issuer, c.ClientID, c.RedirectURL
	v.AutoProvision, v.DefaultRole, v.LinkByEmail, v.SyncRoles, v.TrustIdPMFA = c.AutoProvision, c.DefaultRole, c.LinkByEmail, c.SyncRoles, c.TrustIdPMFA
	if c.AllowedDomains != nil {
		v.Domains = c.AllowedDomains
	}
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE oidc_subject IS NOT NULL`).Scan(&v.LinkedUsers)
	if probe {
		_, _, err := s.oidcInit(ctx)
		ok := err == nil
		v.Reachable = &ok
		if err != nil {
			v.ReachError = truncate(err.Error(), 200)
		}
	}
	return v
}

func splitCSV(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
