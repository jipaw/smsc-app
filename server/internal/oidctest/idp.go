// Package oidctest: penyedia identitas OIDC palsu untuk pengujian (discovery, JWKS, authorize otomatis, token dengan
// verifikasi PKCE). Hanya dipakai oleh tes; tidak diimpor oleh kode produksi.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type grant struct {
	Claims      map[string]any
	Nonce       string
	Challenge   string
	Method      string
	RedirectURI string
	ClientID    string
	Used        bool
}

type IdP struct {
	Server       *httptest.Server
	Issuer       string
	ClientID     string
	ClientSecret string

	mu     sync.Mutex
	key    *rsa.PrivateKey
	other  *rsa.PrivateKey // kunci lain untuk skenario tanda tangan palsu
	grants map[string]*grant

	// Knob skenario (set sebelum langkah token).
	Mutate        func(claims map[string]any) // ubah klaim sebelum ditandatangani
	SignWithOther bool                        // tandatangani dengan kunci yang TIDAK ada di JWKS
	TokenStatus   int                         // bila ≠ 0, endpoint token membalas status ini (mis. 400)
	NoIDToken     bool

	// Pengamatan.
	TokenCalls     int
	LastVerifierOK bool
	LastChallenge  string
	LastMethod     string
}

func New(clientID, clientSecret string) *IdP {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	o, _ := rsa.GenerateKey(rand.Reader, 2048)
	p := &IdP{ClientID: clientID, ClientSecret: clientSecret, key: k, other: o, grants: map[string]*grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("/jwks", p.jwks)
	mux.HandleFunc("/token", p.token)
	p.Server = httptest.NewServer(mux)
	p.Issuer = p.Server.URL
	return p
}

func (p *IdP) Close() { p.Server.Close() }

func (p *IdP) discovery(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer": p.Issuer, "authorization_endpoint": p.Issuer + "/authorize", "token_endpoint": p.Issuer + "/token", "jwks_uri": p.Issuer + "/jwks",
		"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported": []string{"S256"},
	})
}

func (p *IdP) jwks(w http.ResponseWriter, r *http.Request) {
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}}
	_ = json.NewEncoder(w).Encode(set)
}

// Authorize meniru persetujuan pengguna di IdP: membaca parameter dari URL otorisasi yang dikirim aplikasi dan menerbitkan code.
// Mengembalikan code dan parameter yang diamati. claims: klaim dasar (sub wajib); iss/aud/exp/iat/nonce diisi di sini/di token.
func (p *IdP) Authorize(authURL string, claims map[string]any) (code string, q url.Values, err error) {
	u, err := url.Parse(authURL)
	if err != nil {
		return "", nil, err
	}
	q = u.Query()
	if !strings.HasPrefix(authURL, p.Issuer+"/authorize") {
		return "", q, fmt.Errorf("bukan URL otorisasi IdP ini: %s", authURL)
	}
	if q.Get("response_type") != "code" || q.Get("client_id") != p.ClientID {
		return "", q, fmt.Errorf("parameter otorisasi salah: %v", q)
	}
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	code = base64.RawURLEncoding.EncodeToString(b)
	p.mu.Lock()
	p.grants[code] = &grant{Claims: claims, Nonce: q.Get("nonce"), Challenge: q.Get("code_challenge"), Method: q.Get("code_challenge_method"), RedirectURI: q.Get("redirect_uri"), ClientID: q.Get("client_id")}
	p.mu.Unlock()
	return code, q, nil
}

func (p *IdP) token(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.TokenCalls++
	_ = r.ParseForm()
	id, sec, basic := r.BasicAuth()
	if !basic {
		id, sec = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	fail := func(code int, e string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": e})
	}
	if p.TokenStatus != 0 {
		fail(p.TokenStatus, "server_error")
		return
	}
	if id != p.ClientID || (p.ClientSecret != "" && sec != p.ClientSecret) {
		fail(401, "invalid_client")
		return
	}
	g := p.grants[r.PostForm.Get("code")]
	if r.PostForm.Get("grant_type") != "authorization_code" || g == nil || g.Used {
		fail(400, "invalid_grant")
		return
	}
	g.Used = true
	if r.PostForm.Get("redirect_uri") != g.RedirectURI {
		fail(400, "invalid_grant")
		return
	}
	// PKCE S256: BASE64URL(SHA256(code_verifier)) harus sama dengan code_challenge
	h := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	p.LastChallenge, p.LastMethod = g.Challenge, g.Method
	p.LastVerifierOK = g.Method == "S256" && g.Challenge != "" && base64.RawURLEncoding.EncodeToString(h[:]) == g.Challenge
	if !p.LastVerifierOK {
		fail(400, "invalid_grant")
		return
	}
	claims := map[string]any{"iss": p.Issuer, "aud": p.ClientID, "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(), "nonce": g.Nonce}
	for k, v := range g.Claims {
		claims[k] = v
	}
	if p.Mutate != nil {
		p.Mutate(claims)
	}
	w.Header().Set("Content-Type", "application/json")
	out := map[string]any{"access_token": "at-" + r.PostForm.Get("code"), "token_type": "Bearer", "expires_in": 300}
	if !p.NoIDToken {
		out["id_token"] = p.sign(claims)
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (p *IdP) sign(claims map[string]any) string {
	key := p.key
	if p.SignWithOther {
		key = p.other
	}
	sg, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	s, _ := jwt.Signed(sg).Claims(claims).Serialize()
	return s
}
