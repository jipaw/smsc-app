// Package hub: server WebSocket device (protokol v1). Satu sesi aktif per device (S4).
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/jipaw/smsc-app/server/internal/core"
	"github.com/jipaw/smsc-app/server/internal/proto"
)

const (
	subprotocol  = "smsc.v1"
	maxFrame     = 64 * 1024
	maxMsgPerSec = 50
)

type session struct {
	deviceID  uuid.UUID
	conn      *websocket.Conn
	out       chan []byte
	cancel    context.CancelFunc
	pending   sync.Map // id envelope -> chan proto.Envelope (Request)
	since     time.Time
	sessionID uuid.UUID
}

type Hub struct {
	svc *core.Service
	log *slog.Logger
	mu  sync.RWMutex
	ses map[uuid.UUID]*session

	// multi-instance (cluster.go)
	instance uuid.UUID
	rmu      sync.RWMutex
	remote   map[uuid.UUID]uuid.UUID // device -> instance pemilik sesi (selain instance ini)
	rpending sync.Map                // id envelope request -> chan balasan dari instance lain
	kick     chan struct{}
	busOut   chan string
	wakeKick chan struct{}
}

func New(svc *core.Service, log *slog.Logger) *Hub {
	h := &Hub{svc: svc, log: log, ses: map[uuid.UUID]*session{}, instance: uuid.New(), remote: map[uuid.UUID]uuid.UUID{},
		kick: make(chan struct{}, 1), busOut: make(chan string, 1024), wakeKick: make(chan struct{}, 1)}
	svc.SetLink(h)
	return h
}

// ---- core.DeviceLink ----

func (h *Hub) localConnected(id uuid.UUID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.ses[id]
	return ok
}

// Connected: sesi di instance ini, atau di instance lain (menurut device_sessions yang segar).
func (h *Hub) Connected(id uuid.UUID) bool {
	if h.localConnected(id) {
		return true
	}
	_, ok := h.remoteInstance(id)
	return ok
}

func (h *Hub) Send(id uuid.UUID, env proto.Envelope) error {
	if h.localConnected(id) {
		return h.sendLocal(id, env)
	}
	to, ok := h.remoteInstance(id)
	if !ok {
		return core.ErrOffline
	}
	return h.enqueue(context.Background(), to, id, "send", &env, nil, nil, "")
}

func (h *Hub) sendLocal(id uuid.UUID, env proto.Envelope) error {
	h.mu.RLock()
	s, ok := h.ses[id]
	h.mu.RUnlock()
	if !ok {
		return core.ErrOffline
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	select {
	case s.out <- b:
		h.svc.M.WSMessages.WithLabelValues(env.Type, "out").Inc()
		return nil
	default: // backpressure: putus sesi, device akan reconnect dan rekonsiliasi (spec 04 §5)
		s.cancel()
		return errors.New("antrean tulis penuh")
	}
}

func (h *Hub) Request(ctx context.Context, id uuid.UUID, env proto.Envelope, timeout time.Duration) (proto.Envelope, error) {
	if !h.localConnected(id) {
		to, ok := h.remoteInstance(id)
		if !ok {
			return proto.Envelope{}, core.ErrOffline
		}
		ch := make(chan proto.Envelope, 1)
		h.rpending.Store(env.ID, ch)
		defer h.rpending.Delete(env.ID)
		self := h.instance
		if err := h.enqueue(ctx, to, id, "request", &env, &self, nil, ""); err != nil {
			return proto.Envelope{}, err
		}
		select {
		case r := <-ch:
			return r, nil
		case <-time.After(timeout):
			return proto.Envelope{}, errors.New("timeout")
		case <-ctx.Done():
			return proto.Envelope{}, ctx.Err()
		}
	}
	return h.requestLocal(ctx, id, env, timeout)
}

func (h *Hub) requestLocal(ctx context.Context, id uuid.UUID, env proto.Envelope, timeout time.Duration) (proto.Envelope, error) {
	h.mu.RLock()
	s, ok := h.ses[id]
	h.mu.RUnlock()
	if !ok {
		return proto.Envelope{}, core.ErrOffline
	}
	ch := make(chan proto.Envelope, 1)
	s.pending.Store(env.ID, ch)
	defer s.pending.Delete(env.ID)
	if err := h.sendLocal(id, env); err != nil {
		return proto.Envelope{}, err
	}
	select {
	case r := <-ch:
		return r, nil
	case <-time.After(timeout):
		return proto.Envelope{}, errors.New("timeout")
	case <-ctx.Done():
		return proto.Envelope{}, ctx.Err()
	}
}

func (h *Hub) Close(id uuid.UUID, code int, reason string) {
	if !h.localConnected(id) {
		if to, ok := h.remoteInstance(id); ok {
			_ = h.enqueue(context.Background(), to, id, "close", nil, nil, &code, reason)
		}
		return
	}
	h.closeLocal(id, code, reason)
}

func (h *Hub) closeLocal(id uuid.UUID, code int, reason string) {
	h.mu.RLock()
	s, ok := h.ses[id]
	h.mu.RUnlock()
	if ok {
		_ = s.conn.Close(websocket.StatusCode(code), reason)
		s.cancel()
	}
}

// ---- HTTP upgrade ----

func problem(w http.ResponseWriter, status int, typ, title string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": typ, "title": title, "status": status})
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		problem(w, http.StatusUnauthorized, "auth.unauthenticated", "Tidak terautentikasi")
		return
	}
	deviceID, err := h.svc.AuthDevice(r.Context(), strings.TrimPrefix(auth, "Bearer "))
	if err != nil {
		problem(w, http.StatusUnauthorized, "auth.unauthenticated", "Tidak terautentikasi")
		return
	}
	offered := false
	for _, p := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		if strings.TrimSpace(p) == subprotocol {
			offered = true
		}
	}
	if !offered {
		problem(w, http.StatusBadRequest, "ws.subprotocol_required", "Subprotocol smsc.v1 wajib")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{subprotocol}})
	if err != nil {
		return
	}
	conn.SetReadLimit(maxFrame)
	h.run(r.Context(), conn, deviceID)
}

func (h *Hub) run(parent context.Context, conn *websocket.Conn, deviceID uuid.UUID) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	defer cancel()
	s := &session{deviceID: deviceID, conn: conn, out: make(chan []byte, 256), cancel: cancel, since: time.Now(), sessionID: uuid.New()}
	log := h.log.With("device", deviceID)

	// hello wajib pesan pertama (S2), batas 10 dtk
	hctx, hcancel := context.WithTimeout(ctx, 10*time.Second)
	_, raw, err := conn.Read(hctx)
	hcancel()
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "hello expected")
		return
	}
	var env proto.Envelope
	var hello proto.Hello
	if json.Unmarshal(raw, &env) != nil || env.Type != proto.TypeHello || json.Unmarshal(env.Payload, &hello) != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "hello expected")
		return
	}
	if hello.Protocol != proto.Version {
		_ = conn.Close(4426, "protocol version unsupported")
		return
	}

	// satu sesi per device (S4)
	h.mu.Lock()
	if old, ok := h.ses[deviceID]; ok {
		_ = old.conn.Close(4409, "superseded")
		old.cancel()
	}
	h.ses[deviceID] = s
	h.mu.Unlock()
	registered := h.register(ctx, s)
	h.svc.M.WSConnections.Inc()
	h.svc.M.WSConnects.Inc()
	defer func() {
		h.svc.M.WSConnections.Dec()
		h.mu.Lock()
		current := h.ses[deviceID] == s
		if current {
			delete(h.ses, deviceID)
		}
		h.mu.Unlock()
		if registered { // sesi sudah digantikan instance lain => jangan tandai device offline
			current = h.unregister(s) && current
		}
		if current {
			h.svc.OnDisconnected(context.Background(), deviceID)
			log.Info("device terputus")
		}
	}()

	if err := h.svc.OnConnected(ctx, deviceID, hello); err != nil {
		log.Error("OnConnected", "err", err)
		_ = conn.Close(websocket.StatusInternalError, "")
		return
	}
	_ = h.svc.ApplyHelloSims(ctx, deviceID, hello.Sims)
	drop, keep, resend, err := h.svc.Reconcile(ctx, deviceID, hello.Inflight)
	if err != nil {
		log.Error("Reconcile", "err", err)
		_ = conn.Close(websocket.StatusInternalError, "")
		return
	}
	cfg, _ := h.svc.DeviceConfigFor(ctx, deviceID)
	cfgRaw, _ := json.Marshal(map[string]any{"guardrails": cfg.Guardrails})
	ack, _ := proto.New(proto.TypeHelloAck, proto.HelloAck{
		SessionID: uuid.NewString(), HeartbeatIntervalSec: h.svc.Cfg.HeartbeatIntervalSec, ConfigVersion: cfg.ConfigVersion,
		Config: cfgRaw, Reconcile: proto.Reconcile{Drop: drop, Keep: keep},
	}, &env.ID)
	ackBytes, _ := json.Marshal(ack)
	if err := conn.Write(ctx, websocket.MessageText, ackBytes); err != nil {
		return
	}
	log.Info("device online", "inflight", len(hello.Inflight), "pending_events", hello.PendingEvents, "resend", len(resend))

	go h.writer(ctx, s)
	go h.pinger(ctx, s)
	if len(resend) > 0 {
		go h.svc.ResendAssigned(ctx, deviceID, resend)
	}
	h.svc.Wake()

	window, count := time.Now(), 0
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if time.Since(window) > time.Second {
			window, count = time.Now(), 0
		}
		if count++; count > maxMsgPerSec { // T10: rate limit
			p, _ := proto.New(proto.TypeError, proto.ErrorPayload{Code: "rate_limited", Message: "terlalu banyak pesan", Fatal: true}, nil)
			b, _ := json.Marshal(p)
			_ = conn.Write(ctx, websocket.MessageText, b)
			_ = conn.Close(4429, "rate limited")
			return
		}
		var m proto.Envelope
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		h.handle(ctx, s, m)
	}
}

func (h *Hub) writer(ctx context.Context, s *session) {
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-s.out:
			wctx, c := context.WithTimeout(ctx, 10*time.Second)
			err := s.conn.Write(wctx, websocket.MessageText, b)
			c()
			if err != nil {
				s.cancel()
				_ = s.conn.Close(websocket.StatusGoingAway, "write failed")
				return
			}
		}
	}
}

// pinger: ping WS tiap 20 dtk; tanpa pong dalam 2 interval -> putus (protocol §2).
func (h *Hub) pinger(ctx context.Context, s *session) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, c := context.WithTimeout(ctx, 40*time.Second)
			err := s.conn.Ping(pctx)
			c()
			if err != nil {
				_ = s.conn.Close(websocket.StatusGoingAway, "ping timeout")
				s.cancel()
				return
			}
		}
	}
}

func (h *Hub) reply(s *session, typ string, payload any, ref *string) {
	if env, err := proto.New(typ, payload, ref); err == nil {
		_ = h.Send(s.deviceID, env)
	}
}

// knownTypes membatasi kardinalitas label: tipe dari HP tak tepercaya.
var knownTypes = map[string]bool{proto.TypeHeartbeat: true, proto.TypeCommandAck: true, proto.TypeSmsEvent: true, proto.TypeSimReport: true, proto.TypeCommandResult: true, proto.TypeHello: true}

func (h *Hub) handle(ctx context.Context, s *session, m proto.Envelope) {
	lbl := "unknown"
	if knownTypes[m.Type] {
		lbl = m.Type
	}
	h.svc.M.WSMessages.WithLabelValues(lbl, "in").Inc()
	switch m.Type {
	case proto.TypeHeartbeat:
		var hb proto.Heartbeat
		if json.Unmarshal(m.Payload, &hb) == nil {
			h.svc.ApplyHeartbeat(ctx, s.deviceID, hb)
			h.svc.Wake()
		}
	case proto.TypeCommandAck:
		var a proto.CommandAck
		if json.Unmarshal(m.Payload, &a) != nil || m.Ref == nil {
			return
		}
		if ch, ok := s.pending.Load(*m.Ref); ok { // jawaban atas Request (mis. sms.cancel)
			ch.(chan proto.Envelope) <- m
			return
		}
		h.svc.OnCommandAck(ctx, s.deviceID, *m.Ref, a)
	case proto.TypeSmsEvent:
		var ev proto.SmsEvent
		if json.Unmarshal(m.Payload, &ev) != nil {
			return
		}
		if err := h.svc.OnSmsEvent(ctx, s.deviceID, ev); err != nil {
			h.log.Warn("sms.event gagal", "err", err, "event", ev.EventID)
			return // tidak di-ack: device akan mengirim ulang dari outbox
		}
		h.reply(s, proto.TypeEventAck, proto.EventAck{EventIDs: []string{ev.EventID}}, nil)
	case proto.TypeSimReport:
		var r proto.SimReport
		if json.Unmarshal(m.Payload, &r) == nil {
			_ = h.svc.ApplySimReport(ctx, s.deviceID, r)
		}
	case proto.TypeCommandResult:
		var r proto.CommandResult
		if json.Unmarshal(m.Payload, &r) == nil {
			h.svc.OnCommandResult(ctx, s.deviceID, r)
			h.svc.Bus.Publish("device.command_result", map[string]any{"device_id": s.deviceID, "command_id": r.CommandID, "ok": r.OK, "detail": r.Detail})
		}
	default:
		if m.ID != "" {
			h.reply(s, proto.TypeError, proto.ErrorPayload{Code: "unknown_type", Message: "type tidak dikenal: " + m.Type}, &m.ID)
		}
	}
}

// CloseAll menutup semua sesi dengan 1012 (service restart) agar device reconnect dengan backoff.
func (h *Hub) CloseAll() {
	h.mu.RLock()
	list := make([]*session, 0, len(h.ses))
	for _, s := range h.ses {
		list = append(list, s)
	}
	h.mu.RUnlock()
	for _, s := range list {
		_ = s.conn.Close(websocket.StatusServiceRestart, "server restart")
		s.cancel()
	}
}
