package hub

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jipaw/smsc-app/server/internal/proto"
)

// Multi-instance (fase 20). Tabel device_sessions mencatat instance pemilik sesi tiap device; perintah ke device yang sesinya
// ada di instance lain dititipkan lewat tabel hub_commands (durable, tanpa batas ukuran payload) dan LISTEN/NOTIFY hanya
// membangunkan penerima. Penerima juga memeriksa tabel secara berkala, jadi NOTIFY yang hilang tidak menghilangkan perintah.
const (
	chanHub    = "smsc_hub"  // payload: id instance tujuan
	chanBus    = "smsc_bus"  // payload: {o: instance asal, n: nama event, d: data}
	chanWake   = "smsc_wake" // payload: id instance asal
	hbEvery    = 5 * time.Second
	staleAfter = 20 * time.Second
	maxNotify  = 7000 // batas payload NOTIFY PostgreSQL ±8000 byte
)

type busMsg struct {
	O string          `json:"o"`
	N string          `json:"n"`
	D json.RawMessage `json:"d"`
}

// InstanceID mengidentifikasi proses ini di tabel device_sessions/hub_commands.
func (h *Hub) InstanceID() uuid.UUID { return h.instance }

func (h *Hub) remoteInstance(id uuid.UUID) (uuid.UUID, bool) {
	h.rmu.RLock()
	defer h.rmu.RUnlock()
	i, ok := h.remote[id]
	return i, ok
}

func (h *Hub) refreshRemote(ctx context.Context) {
	rows, err := h.svc.Pool.Query(ctx, `SELECT device_id, instance_id FROM device_sessions WHERE instance_id<>$1 AND heartbeat_at > now() - $2::interval`, h.instance, staleAfter.String())
	if err != nil {
		return
	}
	m := map[uuid.UUID]uuid.UUID{}
	for rows.Next() {
		var d, i uuid.UUID
		if rows.Scan(&d, &i) == nil {
			m[d] = i
		}
	}
	rows.Close()
	h.rmu.Lock()
	h.remote = m
	h.rmu.Unlock()
	h.svc.M.HubRemoteSessions.Set(float64(len(m)))
}

// enqueue menitipkan perintah ke instance lain lalu membangunkannya.
func (h *Hub) enqueue(ctx context.Context, to, device uuid.UUID, kind string, env *proto.Envelope, replyTo *uuid.UUID, code *int, reason string) error {
	var raw []byte
	if env != nil {
		raw, _ = json.Marshal(env)
	}
	_, err := h.svc.Pool.Exec(ctx, `WITH i AS (
		INSERT INTO hub_commands(to_instance, device_id, kind, env, reply_instance, code, reason) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id
	) SELECT pg_notify($8, $1::text) FROM i`, to, device, kind, raw, replyTo, code, reason, chanHub)
	res := "ok"
	if err != nil {
		res = "dropped"
	}
	h.svc.M.HubRelay.WithLabelValues(kind, res).Inc()
	return err
}

// register mencatat sesi sebagai milik instance ini dan, bila sesi lama ada di instance lain, memintanya menutup (S4 lintas instance).
func (h *Hub) register(ctx context.Context, s *session) bool {
	var prev *uuid.UUID
	err := pgx.BeginFunc(ctx, h.svc.Pool, func(tx pgx.Tx) error {
		// dua langkah (bukan CTE): CTE penulis dan pembaca pada baris yang sama tidak melihat nilai lama
		if e := tx.QueryRow(ctx, `SELECT instance_id FROM device_sessions WHERE device_id=$1 FOR UPDATE`, s.deviceID).Scan(&prev); e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		_, e := tx.Exec(ctx, `INSERT INTO device_sessions(device_id, instance_id, session_id) VALUES ($1,$2,$3)
			ON CONFLICT (device_id) DO UPDATE SET instance_id=EXCLUDED.instance_id, session_id=EXCLUDED.session_id, connected_at=now(), heartbeat_at=now()`, s.deviceID, h.instance, s.sessionID)
		return e
	})
	if err != nil {
		h.log.Error("hub: catat sesi gagal (hanya lokal)", "err", err)
		return false
	}
	if prev != nil && *prev != h.instance {
		code := 4409
		_ = h.enqueue(ctx, *prev, s.deviceID, "close", nil, nil, &code, "superseded")
	}
	return true
}

// unregister menghapus catatan sesi; true bila catatan itu masih milik sesi ini (belum digantikan instance lain).
func (h *Hub) unregister(s *session) bool {
	tag, err := h.svc.Pool.Exec(context.Background(), `DELETE FROM device_sessions WHERE device_id=$1 AND session_id=$2`, s.deviceID, s.sessionID)
	return err == nil && tag.RowsAffected() > 0
}

// RunCluster menjalankan penerima NOTIFY, poll cadangan, heartbeat sesi, dan penerus event/wake. Panggil sekali per proses.
func (h *Hub) RunCluster(ctx context.Context) {
	tick := h.svc.Cfg.HubTick
	if tick <= 0 {
		tick = time.Second
	}
	h.svc.Bus.SetRelay(h.relayBus)
	h.svc.SetWakeRelay(func() {
		select {
		case h.wakeKick <- struct{}{}:
		default:
		}
	})
	defer func() {
		_, _ = h.svc.Pool.Exec(context.Background(), `DELETE FROM device_sessions WHERE instance_id=$1`, h.instance)
	}()
	go h.sender(ctx)
	go h.listen(ctx)

	t := time.NewTicker(tick)
	defer t.Stop()
	lastHB, lastJan := time.Time{}, time.Now()
	for {
		h.refreshRemote(ctx)
		h.drain(ctx)
		if time.Since(lastHB) >= hbEvery {
			_, _ = h.svc.Pool.Exec(ctx, `UPDATE device_sessions SET heartbeat_at=now() WHERE instance_id=$1`, h.instance)
			lastHB = time.Now()
		}
		if time.Since(lastJan) >= 30*time.Second {
			h.janitor(ctx)
			lastJan = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-h.kick:
		}
	}
}

func (h *Hub) janitor(ctx context.Context) {
	_, _ = h.svc.Pool.Exec(ctx, `DELETE FROM device_sessions WHERE heartbeat_at < now() - interval '60 seconds'`)
	_, _ = h.svc.Pool.Exec(ctx, `DELETE FROM hub_commands WHERE created_at < now() - interval '60 seconds'`)
	_, _ = h.svc.Pool.Exec(ctx, `DELETE FROM cmd_refs WHERE created_at < now() - interval '1 hour'`)
}

// listen menampung NOTIFY pada koneksi khusus; saat terputus, menyambung ulang (poll cadangan menutup celahnya).
func (h *Hub) listen(ctx context.Context) {
	for ctx.Err() == nil {
		h.listenOnce(ctx)
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

func (h *Hub) listenOnce(ctx context.Context) {
	conn, err := h.svc.Pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer func() {
		if ctx.Err() != nil || conn.Conn().IsClosed() {
			_ = conn.Conn().Close(context.Background())
		}
		conn.Release()
	}()
	for _, c := range []string{chanHub, chanBus, chanWake} {
		if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{c}.Sanitize()); err != nil {
			return
		}
	}
	select { // notifikasi yang terlewat selama tidak mendengarkan: periksa tabel sekarang
	case h.kick <- struct{}{}:
	default:
	}
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			_ = conn.Conn().Close(context.Background()) // koneksi rusak: buang dari pool
			return
		}
		switch n.Channel {
		case chanHub:
			if n.Payload == h.instance.String() {
				select {
				case h.kick <- struct{}{}:
				default:
				}
			}
		case chanBus:
			var m busMsg
			if json.Unmarshal([]byte(n.Payload), &m) == nil && m.O != h.instance.String() {
				h.svc.Bus.PublishRaw(m.N, m.D, false)
			}
		case chanWake:
			if n.Payload != h.instance.String() {
				h.svc.WakeLocal()
			}
		}
	}
}

// sender mengirim NOTIFY bus/wake secara berurutan di luar jalur permintaan (tidak memblokir Publish/Wake).
func (h *Hub) sender(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-h.busOut:
			if _, err := h.svc.Pool.Exec(ctx, `SELECT pg_notify($1,$2)`, chanBus, p); err != nil {
				h.svc.M.HubRelay.WithLabelValues("bus", "dropped").Inc()
			} else {
				h.svc.M.HubRelay.WithLabelValues("bus", "ok").Inc()
			}
		case <-h.wakeKick:
			if _, err := h.svc.Pool.Exec(ctx, `SELECT pg_notify($1,$2)`, chanWake, h.instance.String()); err != nil {
				h.svc.M.HubRelay.WithLabelValues("wake", "dropped").Inc()
			} else {
				h.svc.M.HubRelay.WithLabelValues("wake", "ok").Inc()
			}
			time.Sleep(20 * time.Millisecond) // gabungkan Wake beruntun
		}
	}
}

func (h *Hub) relayBus(name string, raw []byte) {
	b, _ := json.Marshal(busMsg{O: h.instance.String(), N: name, D: raw})
	if len(b) > maxNotify {
		h.svc.M.HubRelay.WithLabelValues("bus", "dropped").Inc()
		return
	}
	select {
	case h.busOut <- string(b):
	default:
		h.svc.M.HubRelay.WithLabelValues("bus", "dropped").Inc()
	}
}

type cmdRow struct {
	ID    int64
	Dev   uuid.UUID
	Kind  string
	Env   []byte
	Reply *uuid.UUID
	Code  *int
	Why   *string
}

// drain mengambil dan menjalankan perintah yang ditujukan ke instance ini, berurutan menurut id.
func (h *Hub) drain(ctx context.Context) {
	for ctx.Err() == nil {
		rows, err := h.svc.Pool.Query(ctx, `DELETE FROM hub_commands WHERE id IN (
			SELECT id FROM hub_commands WHERE to_instance=$1 ORDER BY id LIMIT 200 FOR UPDATE SKIP LOCKED
		) RETURNING id, device_id, kind, env, reply_instance, code, reason`, h.instance)
		if err != nil {
			return
		}
		var list []cmdRow
		for rows.Next() {
			var c cmdRow
			if rows.Scan(&c.ID, &c.Dev, &c.Kind, &c.Env, &c.Reply, &c.Code, &c.Why) == nil {
				list = append(list, c)
			}
		}
		rows.Close()
		for i := 1; i < len(list); i++ { // RETURNING tidak menjamin urutan: insertion sort (daftar kecil)
			for j := i; j > 0 && list[j].ID < list[j-1].ID; j-- {
				list[j], list[j-1] = list[j-1], list[j]
			}
		}
		for _, c := range list {
			h.exec(ctx, c)
		}
		if len(list) < 200 {
			return
		}
	}
}

func (h *Hub) exec(ctx context.Context, c cmdRow) {
	var env proto.Envelope
	if len(c.Env) > 0 {
		if json.Unmarshal(c.Env, &env) != nil {
			return
		}
	}
	switch c.Kind {
	case "send":
		_ = h.sendLocal(c.Dev, env)
	case "close":
		code, why := 1000, ""
		if c.Code != nil {
			code = *c.Code
		}
		if c.Why != nil {
			why = *c.Why
		}
		h.closeLocal(c.Dev, code, why)
	case "request":
		go func() {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			resp, err := h.requestLocal(rctx, c.Dev, env, 3*time.Second)
			if err == nil && c.Reply != nil {
				_ = h.enqueue(rctx, *c.Reply, c.Dev, "reply", &resp, nil, nil, "")
			}
		}()
	case "reply":
		if env.Ref != nil {
			if ch, ok := h.rpending.Load(*env.Ref); ok {
				select {
				case ch.(chan proto.Envelope) <- env:
				default:
				}
			}
		}
	}
}
