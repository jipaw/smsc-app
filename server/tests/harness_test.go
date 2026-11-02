package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jipaw/smsc-app/server/internal/api"
	"github.com/jipaw/smsc-app/server/internal/bus"
	"github.com/jipaw/smsc-app/server/internal/config"
	"github.com/jipaw/smsc-app/server/internal/core"
	"github.com/jipaw/smsc-app/server/internal/db"
	"github.com/jipaw/smsc-app/server/internal/hub"
	"github.com/jipaw/smsc-app/server/internal/proto"
)

// env wajib: SMSC_TEST_DATABASE_URL (mis. postgres://nobody@localhost:55432/postgres?sslmode=disable). Tanpa itu, tes dilewati.
type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	svc    *core.Service
	hub    *hub.Hub
	srv    *httptest.Server
	cancel context.CancelFunc
}

func newEnv(t *testing.T) *env { return newEnvOpts(t) }

func newEnvOpts(t *testing.T, opts ...func(*config.Config)) *env {
	t.Helper()
	base := os.Getenv("SMSC_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("SMSC_TEST_DATABASE_URL tidak diset")
	}
	ctx, cancel := context.WithCancel(context.Background())
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	name := "smsc_it_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	dsn := strings.Replace(base, "/postgres?", "/"+name+"?", 1)
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var logw io.Writer = io.Discard
	if os.Getenv("SMSC_TEST_LOG") != "" {
		logw = os.Stderr
	}
	log := slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := db.Migrate(ctx, pool, log); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DatabaseURL: dsn, PublicBaseURL: "http://example.test", SessionSecret: strings.Repeat("s", 40), DefaultRegion: "ID",
		DispatchTick: 150 * time.Millisecond, ReaperInterval: 150 * time.Millisecond, OfflineAfter: 90 * time.Second,
		HeartbeatIntervalSec: 30, EnrollTTL: 15 * time.Minute, RequireVPN: false,
		CampaignTick: 150 * time.Millisecond, CampaignQueueCap: 20000, NoCapacityAfter: 10 * time.Minute}
	for _, o := range opts {
		o(&cfg)
	}
	b := bus.New(5 * time.Minute)
	svc := core.New(pool, cfg, log, b)
	h := hub.New(svc, log)
	srv := httptest.NewServer(api.New(svc, h, b, log).Router())
	e := &env{t: t, pool: pool, svc: svc, hub: h, srv: srv, cancel: cancel}
	go svc.RunDispatcher(ctx)
	go svc.RunReaper(ctx)
	go svc.RunCampaigns(ctx)
	// policy default cepat untuk tes: delay 300 ms, tanpa batas rate, jendela sepanjang hari, timeout pendek
	e.sql(`UPDATE dispatch_policies SET min_delay_ms=300, max_delay_ms=300, burst_size=100000, burst_pause_ms=0,
		rate_per_minute=NULL, rate_per_hour=NULL, rate_per_day=NULL, ack_timeout_ms=1500, sent_timeout_ms=3000,
		retry_backoff_ms=1000, retry_backoff_max_ms=2000, max_attempts=3,
		send_window='{"timezone":"UTC","windows":[{"days":[1,2,3,4,5,6,7],"start":"00:00","end":"00:00"}]}' WHERE is_default`)
	t.Cleanup(func() {
		cancel()
		srv.Close()
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close()
	})
	return e
}

func (e *env) sql(q string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), q, args...); err != nil {
		e.t.Fatalf("sql: %v\n%s", err, q)
	}
}

// ---------- klien HTTP dengan sesi ----------

type client struct {
	e    *env
	http *http.Client
	csrf string
	key  string
}

func (e *env) loginAs(role string) *client {
	e.t.Helper()
	email := role + "-" + uuid.NewString()[:6] + "@example.com"
	if _, err := e.svc.CreateUser(context.Background(), email, "password-panjang-123", role); err != nil {
		e.t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	c := &client{e: e, http: &http.Client{Jar: jar}}
	var out struct {
		CSRF string `json:"csrf_token"`
	}
	st := c.do("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": "password-panjang-123"}, &out)
	if st != 200 {
		e.t.Fatalf("login %s: status %d", role, st)
	}
	c.csrf = out.CSRF
	return c
}

func (c *client) do(method, path string, body any, out any) int {
	c.e.t.Helper()
	st, _ := c.doH(method, path, body, out, nil)
	return st
}

func (c *client) doH(method, path string, body any, out any, hdr map[string]string) (int, http.Header) {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" && method != "GET" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	if resp.StatusCode == 429 { // hormati Retry-After seperti klien nyata, lalu ulangi sekali
		resp.Body.Close()
		time.Sleep(1100 * time.Millisecond)
		if rd != nil {
			b, _ := json.Marshal(body)
			req.Body = io.NopCloser(bytes.NewReader(b))
		}
		if resp, err = c.http.Do(req); err != nil {
			c.e.t.Fatal(err)
		}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			c.e.t.Fatalf("decode %s %s: %v\n%s", method, path, err, raw)
		}
	}
	return resp.StatusCode, resp.Header
}

// ---------- simulator device ----------

type Sim struct {
	e        *env
	t        *testing.T
	DeviceID uuid.UUID
	Secret   string
	conn     *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	Sends    []sendRec // sms.send yang diterima
	Cancels  []string
	Acks     []string // event_id yang di-ack server
	// perilaku (dapat diubah per tes)
	OnSend func(s *Sim, env proto.Envelope, cmd proto.SmsSend)
	Closed chan websocket.CloseError
}

type sendRec struct {
	At  time.Time
	Env proto.Envelope
	Cmd proto.SmsSend
}

func (e *env) enroll(admin *client, slots ...int) (uuid.UUID, string) {
	e.t.Helper()
	var en struct {
		Token string `json:"token"`
	}
	if st := admin.do("POST", "/api/v1/device-enrollments", map[string]any{"device_name": "HP Uji"}, &en); st != 201 {
		e.t.Fatalf("enrollment status %d", st)
	}
	if len(slots) == 0 {
		slots = []int{0}
	}
	sims := []map[string]any{}
	for _, s := range slots {
		sims = append(sims, map[string]any{"slot_index": s, "subscription_id": 10 + s, "carrier_name": fmt.Sprintf("Op%d", s), "is_esim": false, "registered": true})
	}
	var out struct {
		DeviceID string `json:"device_id"`
		Secret   string `json:"device_secret"`
	}
	st := admin.do("POST", "/api/v1/device/enroll", map[string]any{
		"token": en.Token, "install_id": uuid.NewString(),
		"device_info":  map[string]any{"manufacturer": "Samsung", "model": "A14", "android_version": "13", "sdk_int": 33, "app_version": "poc"},
		"capabilities": map[string]any{}, "sims": sims}, &out)
	if st != 201 {
		e.t.Fatalf("enroll status %d", st)
	}
	id, _ := uuid.Parse(out.DeviceID)
	return id, out.Secret
}

// connect membuka WS, mengirim hello, dan menunggu hello.ack. inflight: job yang diklaim device.
func (e *env) connect(id uuid.UUID, secret string, inflight []proto.Inflight) (*Sim, proto.HelloAck) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/ws/device", &websocket.DialOptions{
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + secret}},
		Subprotocols: []string{"smsc.v1"},
	})
	if err != nil {
		cancel()
		e.t.Fatalf("dial: %v", err)
	}
	s := &Sim{e: e, t: e.t, DeviceID: id, Secret: secret, conn: conn, ctx: ctx, cancel: cancel, Closed: make(chan websocket.CloseError, 1)}
	hello, _ := proto.New(proto.TypeHello, proto.Hello{AppVersion: "poc", Protocol: 1, Inflight: inflight,
		Sims: []proto.SimRuntime{{SlotIndex: 0, Registered: ptrB(true)}}}, nil)
	s.write(hello)
	_, raw, err := conn.Read(ctx)
	if err != nil {
		cancel()
		e.t.Fatalf("hello.ack: %v", err)
	}
	var env proto.Envelope
	_ = json.Unmarshal(raw, &env)
	if env.Type != proto.TypeHelloAck {
		e.t.Fatalf("mau hello.ack, dapat %s", env.Type)
	}
	var ack proto.HelloAck
	_ = json.Unmarshal(env.Payload, &ack)
	go s.loop()
	e.t.Cleanup(func() { cancel(); _ = conn.CloseNow() })
	return s, ack
}

func ptrB(b bool) *bool     { return &b }
func ptrI(i int) *int       { return &i }
func ptrS(s string) *string { return &s }

func (s *Sim) write(env proto.Envelope) {
	b, _ := json.Marshal(env)
	if err := s.conn.Write(s.ctx, websocket.MessageText, b); err != nil && s.ctx.Err() == nil {
		s.t.Logf("sim write: %v", err)
	}
}

func (s *Sim) loop() {
	for {
		_, raw, err := s.conn.Read(s.ctx)
		if err != nil {
			var ce websocket.CloseError
			if e, ok := err.(websocket.CloseError); ok {
				ce = e
			} else {
				ce = websocket.CloseError{Code: websocket.CloseStatus(err)}
			}
			select {
			case s.Closed <- ce:
			default:
			}
			return
		}
		var env proto.Envelope
		if json.Unmarshal(raw, &env) != nil {
			continue
		}
		switch env.Type {
		case proto.TypeSmsSend:
			var cmd proto.SmsSend
			_ = json.Unmarshal(env.Payload, &cmd)
			s.mu.Lock()
			s.Sends = append(s.Sends, sendRec{time.Now(), env, cmd})
			s.mu.Unlock()
			if s.OnSend != nil {
				s.OnSend(s, env, cmd)
			}
		case proto.TypeSmsCancel:
			var c proto.SmsCancel
			_ = json.Unmarshal(env.Payload, &c)
			s.mu.Lock()
			s.Cancels = append(s.Cancels, c.MessageID)
			s.mu.Unlock()
			s.Ack(env, true, "")
		case proto.TypeEventAck:
			var a proto.EventAck
			_ = json.Unmarshal(env.Payload, &a)
			s.mu.Lock()
			s.Acks = append(s.Acks, a.EventIDs...)
			s.mu.Unlock()
		}
	}
}

func (s *Sim) Ack(env proto.Envelope, accepted bool, reason string) {
	var r *string
	if reason != "" {
		r = &reason
	}
	a, _ := proto.New(proto.TypeCommandAck, proto.CommandAck{Accepted: accepted, Reason: r}, &env.ID)
	s.write(a)
}

func (s *Sim) Event(cmd proto.SmsSend, state string, errCode string) string {
	id := uuid.NewString()
	ev := proto.SmsEvent{EventID: id, MessageID: cmd.MessageID, Attempt: cmd.Attempt, State: state, PartsTotal: 1, At: time.Now().UTC().Format(time.RFC3339Nano)}
	if errCode != "" {
		ev.ErrorCode = &errCode
	}
	e, _ := proto.New(proto.TypeSmsEvent, ev, nil)
	s.write(e)
	return id
}

func (s *Sim) EventRaw(ev proto.SmsEvent) {
	e, _ := proto.New(proto.TypeSmsEvent, ev, nil)
	s.write(e)
}

// Happy: perilaku normal - ack, lalu sending -> sent -> delivered.
func Happy(s *Sim, env proto.Envelope, cmd proto.SmsSend) {
	s.Ack(env, true, "")
	s.Event(cmd, "sending", "")
	s.Event(cmd, "sent", "")
	s.Event(cmd, "delivered", "")
}

func (s *Sim) SendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Sends)
}

// ---------- util tunggu ----------

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	dl := time.Now().Add(d)
	for time.Now().Before(dl) {
		if cond() {
			return
		}
		time.Sleep(150 * time.Millisecond) // < batas rate sesi (10/dtk)
	}
	t.Fatalf("timeout menunggu: %s", what)
}

type msgView struct {
	ID        string  `json:"id"`
	Status    string  `json:"status"`
	Attempt   int     `json:"attempt"`
	To        string  `json:"to"`
	ErrorCode *string `json:"error_code"`
	Events    []struct {
		Type    string `json:"type"`
		Attempt int    `json:"attempt"`
	} `json:"events"`
}

func (c *client) msg(id string) msgView {
	var m msgView
	if st := c.do("GET", "/api/v1/messages/"+id, nil, &m); st != 200 {
		c.e.t.Fatalf("get message %s: %d", id, st)
	}
	return m
}

func (c *client) send(to string, body string, extras ...map[string]any) []msgView {
	c.e.t.Helper()
	req := map[string]any{"recipients": []map[string]any{{"to": to}}, "body": body}
	for _, extra := range extras {
		for k, v := range extra {
			req[k] = v
		}
	}
	var out struct {
		Messages []msgView `json:"messages"`
	}
	if st := c.do("POST", "/api/v1/messages", req, &out); st != 202 {
		c.e.t.Fatalf("POST /messages: %d", st)
	}
	return out.Messages
}

func (c *client) waitStatus(id string, want string, d time.Duration) msgView {
	c.e.t.Helper()
	var m msgView
	waitFor(c.e.t, d, fmt.Sprintf("pesan %s -> %s", id[:8], want), func() bool { m = c.msg(id); return m.Status == want })
	return m
}
