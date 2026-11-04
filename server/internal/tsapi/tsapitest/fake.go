// Package tsapitest: Tailscale API palsu (OAuth token + keys) untuk tes dan lab.
package tsapitest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

type CreatedKey struct {
	ID            string
	Key           string
	Tags          []string
	Reusable      bool
	Ephemeral     bool
	Preauthorized bool
	ExpirySeconds int
	Description   string
}

type Fake struct {
	*httptest.Server
	ClientID, ClientSecret string
	// FixedKey: nilai secret yang dikembalikan (mis. sama dengan RequireAuthKey testcontrol); kosong = acak berurutan
	FixedKey string

	mu          sync.Mutex
	TokenCalls  int
	Created     []CreatedKey
	Deleted     []string
	FailCreates int // jumlah pembuatan berikutnya yang dibalas 500
	tokens      map[string]bool
}

func New(clientID, clientSecret string) *Fake {
	f := &Fake{ClientID: clientID, ClientSecret: clientSecret, tokens: map[string]bool{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *Fake) Snapshot() (tokenCalls int, created []CreatedKey, deleted []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.TokenCalls, append([]CreatedKey(nil), f.Created...), append([]string(nil), f.Deleted...)
}

func (f *Fake) SetFailCreates(n int) { f.mu.Lock(); f.FailCreates = n; f.mu.Unlock() }

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/api/v2/oauth/token" {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != f.ClientID || r.Form.Get("client_secret") != f.ClientSecret {
			jsonErr(w, 401, "invalid client")
			return
		}
		f.TokenCalls++
		tok := fmt.Sprintf("tok-%d", f.TokenCalls)
		f.tokens[tok] = true
		json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 3600})
		return
	}
	if !f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")] {
		jsonErr(w, 401, "unauthorized")
		return
	}
	if r.Method == "POST" && r.URL.Path == "/api/v2/tailnet/-/keys" {
		if f.FailCreates > 0 {
			f.FailCreates--
			jsonErr(w, 500, "internal error")
			return
		}
		var in struct {
			Capabilities struct {
				Devices struct {
					Create struct {
						Reusable, Ephemeral, Preauthorized bool
						Tags                               []string
					}
				}
			}
			ExpirySeconds int    `json:"expirySeconds"`
			Description   string `json:"description"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		c := in.Capabilities.Devices.Create
		k := CreatedKey{ID: fmt.Sprintf("k%d", len(f.Created)+1), Tags: c.Tags, Reusable: c.Reusable, Ephemeral: c.Ephemeral,
			Preauthorized: c.Preauthorized, ExpirySeconds: in.ExpirySeconds, Description: in.Description}
		k.Key = f.FixedKey
		if k.Key == "" {
			k.Key = "tskey-auth-" + k.ID + "-rahasia"
		}
		f.Created = append(f.Created, k)
		json.NewEncoder(w).Encode(map[string]any{"id": k.ID, "key": k.Key, "expires": time.Now().Add(time.Duration(in.ExpirySeconds) * time.Second)})
		return
	}
	if r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v2/tailnet/-/keys/") {
		id := strings.TrimPrefix(r.URL.Path, "/api/v2/tailnet/-/keys/")
		for _, d := range f.Deleted {
			if d == id {
				jsonErr(w, 404, "not found")
				return
			}
		}
		f.Deleted = append(f.Deleted, id)
		w.WriteHeader(200)
		return
	}
	jsonErr(w, 404, "not found")
}
