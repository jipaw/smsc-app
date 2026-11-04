// Package tsapi: klien minimal Tailscale API v2 untuk membuat dan menghapus auth key device.
// Autentikasi: OAuth client (client_credentials, scope auth_keys; token di-cache sampai mendekati kedaluwarsa) atau API key.
package tsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Client struct {
	BaseURL      string // https://api.tailscale.com
	Tailnet      string // "-" = tailnet milik kredensial
	ClientID     string
	ClientSecret string
	APIKey       string
	HTTP         *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// Key: auth key hasil pembuatan. Secret hanya ada pada respons pembuatan.
type Key struct {
	ID      string    `json:"id"`
	Key     string    `json:"key"`
	Expires time.Time `json:"expires"`
}

// Error: respons non-2xx dari Tailscale API (pesan dari API disertakan; tidak pernah memuat kredensial kita).
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("tailscale API %d: %s", e.Status, e.Message) }

func (c *Client) httpc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (c *Client) bearer(ctx context.Context) (string, error) {
	if c.APIKey != "" {
		return c.APIKey, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.tokenExp) > time.Minute {
		return c.token, nil
	}
	form := url.Values{"client_id": {c.ClientID}, "client_secret": {c.ClientSecret}, "grant_type": {"client_credentials"}}
	req, _ := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/api/v2/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := c.do(req, &out); err != nil {
		return "", fmt.Errorf("token OAuth: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("token OAuth kosong")
	}
	c.token, c.tokenExp = out.AccessToken, time.Now().Add(time.Duration(out.ExpiresIn)*time.Second)
	return c.token, nil
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.httpc().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Message == "" {
			e.Message = http.StatusText(resp.StatusCode)
		}
		return &Error{Status: resp.StatusCode, Message: e.Message}
	}
	if out != nil && len(body) > 0 {
		return json.Unmarshal(body, out)
	}
	return nil
}

func (c *Client) authed(ctx context.Context, method, path string, body any, out any) error {
	tok, err := c.bearer(ctx)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	err = c.do(req, out)
	var ae *Error
	if c.APIKey == "" && asErr(err, &ae) && ae.Status == http.StatusUnauthorized { // token dicabut lebih awal: buang cache
		c.mu.Lock()
		c.token = ""
		c.mu.Unlock()
	}
	return err
}

func asErr(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

// CreateDeviceKey: auth key sekali pakai, pre-authorized, ber-tag, tidak ephemeral, berlaku `expiry`.
func (c *Client) CreateDeviceKey(ctx context.Context, tags []string, expiry time.Duration, description string) (Key, error) {
	body := map[string]any{
		"capabilities": map[string]any{"devices": map[string]any{"create": map[string]any{
			"reusable": false, "ephemeral": false, "preauthorized": true, "tags": tags,
		}}},
		"expirySeconds": int(expiry.Seconds()),
		"description":   description,
	}
	var k Key
	err := c.authed(ctx, "POST", "/api/v2/tailnet/"+url.PathEscape(c.Tailnet)+"/keys", body, &k)
	if err == nil && (k.ID == "" || k.Key == "") {
		err = fmt.Errorf("respons Tailscale API tanpa id/key")
	}
	return k, err
}

// DeleteKey mencabut auth key (404 dianggap sudah tidak ada).
func (c *Client) DeleteKey(ctx context.Context, id string) error {
	err := c.authed(ctx, "DELETE", "/api/v2/tailnet/"+url.PathEscape(c.Tailnet)+"/keys/"+url.PathEscape(id), nil, nil)
	var ae *Error
	if asErr(err, &ae) && ae.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// CheckAuth memastikan kredensial OAuth dapat menukar token (API key tidak diuji tanpa memanggil endpoint lain).
func (c *Client) CheckAuth(ctx context.Context) error {
	_, err := c.bearer(ctx)
	return err
}
