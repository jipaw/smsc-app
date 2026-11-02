package core

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jipaw/smsc-app/server/internal/policy"
	"github.com/jipaw/smsc-app/server/internal/proto"
)

type dbx interface {
	queryer
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// closeLane: dipanggil saat pesan melepas slot SIM. Menghitung jeda acak [min,max] (+ jeda burst) -> sims.next_eligible_at (ADR-04, spec 04 §4.5).
func (s *Service) closeLane(ctx context.Context, db dbx, simID uuid.UUID, msgPolicy *uuid.UUID) error {
	var simPol, devPol *uuid.UUID
	var burst int
	if err := db.QueryRow(ctx, `SELECT s.policy_id, d.policy_id, s.burst_count FROM sims s JOIN devices d ON d.id=s.device_id WHERE s.id=$1`, simID).Scan(&simPol, &devPol, &burst); err != nil {
		return err
	}
	eff, err := s.Effective(ctx, db, devPol, simPol, msgPolicy)
	if err != nil && !errors.Is(err, policy.ErrEmptyWindow) {
		return err
	}
	delay := eff.MinDelayMs
	if eff.MaxDelayMs > eff.MinDelayMs {
		delay += rand.IntN(eff.MaxDelayMs - eff.MinDelayMs + 1)
	}
	burst++
	if eff.BurstSize > 0 && burst%eff.BurstSize == 0 {
		delay += eff.BurstPauseMs
	}
	_, err = db.Exec(ctx, `UPDATE sims SET burst_count=$2,
		next_eligible_at = GREATEST(COALESCE(next_eligible_at, now()), now() + ($3::int * interval '1 millisecond')) WHERE id=$1`, simID, burst, delay)
	return err
}

// ---------- kill switch ----------

func (s *Service) Paused(ctx context.Context) bool {
	var v bool
	if err := s.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT (value)::boolean FROM settings WHERE key='paused'), false)`).Scan(&v); err != nil {
		return false
	}
	return v
}

func (s *Service) SetPaused(ctx context.Context, on bool, actor string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO settings(key,value) VALUES ('paused',to_jsonb($1::boolean)) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, on)
	if err == nil {
		s.Audit(ctx, actor, "dispatch.pause", "settings", "paused", map[string]any{"paused": on})
		s.Bus.Publish("settings.updated", map[string]any{"paused": on})
		s.Wake()
	}
	return err
}

// ---------- dispatcher ----------

type lane struct {
	simID, deviceID   uuid.UUID
	slot              int
	next              *time.Time
	simPolicy, devPol *uuid.UUID
	devStatus         string
	devReason         *string
}

// RunDispatcher: event-driven (Wake) + tick pengaman + timer ke waktu siap terdekat.
func (s *Service) RunDispatcher(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	tick := time.NewTicker(s.Cfg.DispatchTick)
	defer timer.Stop()
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-tick.C:
		case <-timer.C:
		}
		next := s.dispatchOnce(ctx)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		if !next.IsZero() {
			d := time.Until(next)
			if d < 10*time.Millisecond {
				d = 10 * time.Millisecond
			}
			timer.Reset(d)
		} else {
			timer.Reset(time.Hour)
		}
	}
}

// dispatchOnce melepas paling banyak satu pesan per lane siap dan mengembalikan waktu terdekat lane berikutnya siap.
func (s *Service) dispatchOnce(ctx context.Context) (earliest time.Time) {
	consider := func(t time.Time) {
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	if s.link == nil || s.Paused(ctx) {
		return
	}
	rows, err := s.Pool.Query(ctx, `SELECT s.id, s.device_id, s.slot_index, s.next_eligible_at, s.policy_id, d.policy_id, d.status::text, d.status_reason
		FROM sims s JOIN devices d ON d.id=s.device_id
		WHERE s.enabled AND s.status='active' AND d.enabled AND d.status IN ('online','degraded') ORDER BY s.next_eligible_at NULLS FIRST, s.id`)
	if err != nil {
		s.Log.Error("dispatcher: load lanes", "err", err)
		return
	}
	var lanes []lane
	for rows.Next() {
		var l lane
		if err := rows.Scan(&l.simID, &l.deviceID, &l.slot, &l.next, &l.simPolicy, &l.devPol, &l.devStatus, &l.devReason); err != nil {
			rows.Close()
			return
		}
		lanes = append(lanes, l)
	}
	rows.Close()
	for _, l := range lanes {
		if !s.link.Connected(l.deviceID) {
			continue
		}
		if l.devStatus == "degraded" && l.devReason != nil && (*l.devReason == "permission_denied" || *l.devReason == "no_sim_registered") {
			continue
		}
		if t, ok := s.tryLane(ctx, l); !ok && !t.IsZero() {
			consider(t)
		}
	}
	return
}

// tryLane mengembalikan (waktuSiapBerikut, dilepas). waktu nol = tidak ada pewaktu (menunggu event).
func (s *Service) tryLane(ctx context.Context, l lane) (time.Time, bool) {
	eff, err := s.Effective(ctx, s.Pool, l.devPol, l.simPolicy, nil)
	if err != nil {
		return time.Time{}, false // mis. irisan jendela kosong
	}
	now := time.Now()
	if l.next != nil && l.next.After(now) {
		return *l.next, false
	}
	if len(eff.Windows) > 0 && !policy.AllAllow(eff.Windows, now) {
		if t, ok := policy.NextOpen(eff.Windows, now); ok {
			return t, false
		}
		return time.Time{}, false
	}
	// pemeriksaan otoritatif (next_eligible_at, inflight, rate) dilakukan di dalam transaksi penugasan
	// di bawah kunci baris SIM, agar tidak balapan dengan event 'sent' yang menutup lane.
	mid, wait, ok := s.assignNext(ctx, l, eff)
	if !ok {
		return wait, false
	}
	if eff.MaxInflightPerSim > 1 { // jeda diterapkan antar lease bila konkurensi > 1
		_ = s.closeLane(ctx, s.Pool, l.simID, nil)
	}
	s.sendToDevice(ctx, l, mid, eff)
	return time.Time{}, true
}

const pickSQL = `
WITH c AS (
  SELECT m.id FROM messages m
  WHERE m.status='queued' AND m.not_before <= now() AND m.expires_at > now()
    AND (m.campaign_id IS NULL OR EXISTS (SELECT 1 FROM campaigns c WHERE c.id=m.campaign_id AND c.status='running'
         AND (cardinality(c.sim_ids)=0 OR $1::uuid = ANY(c.sim_ids))))
    AND (cardinality(m.route_sim_ids)=0 OR $1::uuid = ANY(m.route_sim_ids))
    AND (cardinality(m.route_device_ids)=0 OR $2::uuid = ANY(m.route_device_ids))
  ORDER BY m.priority DESC, m.not_before, m.id
  FOR UPDATE OF m SKIP LOCKED LIMIT 1)
UPDATE messages m SET status='assigned', sim_id=$1, device_id=$2, attempt=GREATEST(m.attempt,1), assigned_at=now(),
       lease_expires_at = now() + ($3::int * interval '1 millisecond'), error_code=NULL
FROM c WHERE m.id=c.id RETURNING m.id`

// assignNext: dalam satu transaksi - kunci baris SIM, periksa ulang jeda/inflight/rate, lalu ambil pesan (SKIP LOCKED).
// Mengembalikan (id, waktuSiapBerikut, ditugaskan).
func (s *Service) assignNext(ctx context.Context, l lane, eff policy.Effective) (uuid.UUID, time.Time, bool) {
	var id uuid.UUID
	var wait time.Time
	assigned := false
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var next *time.Time
		if err := tx.QueryRow(ctx, `SELECT next_eligible_at FROM sims WHERE id=$1 FOR UPDATE`, l.simID).Scan(&next); err != nil {
			return err
		}
		now := time.Now()
		if next != nil && next.After(now) {
			wait = *next
			return nil
		}
		var inflight int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM messages WHERE sim_id=$1 AND status IN ('assigned','sending')`, l.simID).Scan(&inflight); err != nil {
			return err
		}
		if inflight >= eff.MaxInflightPerSim {
			return nil // menunggu event; Wake() dipicu saat pesan melepas slot
		}
		for _, r := range []struct {
			d     time.Duration
			limit int
		}{{time.Minute, eff.RatePerMinute}, {time.Hour, eff.RatePerHour}, {24 * time.Hour, eff.RatePerDay}} {
			if r.limit <= 0 {
				continue
			}
			since := now.Add(-r.d)
			var cnt int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM messages WHERE sim_id=$1 AND sent_at > $2`, l.simID, since).Scan(&cnt); err != nil {
				return err
			}
			if cnt >= r.limit {
				var oldest time.Time
				if err := tx.QueryRow(ctx, `SELECT sent_at FROM messages WHERE sim_id=$1 AND sent_at > $2 ORDER BY sent_at OFFSET $3 LIMIT 1`, l.simID, since, cnt-r.limit).Scan(&oldest); err != nil {
					return err
				}
				if w := oldest.Add(r.d); w.After(wait) {
					wait = w
				}
			}
		}
		if !wait.IsZero() {
			return nil
		}
		if err := tx.QueryRow(ctx, pickSQL, l.simID, l.deviceID, eff.AckTimeoutMs).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		var attempt int
		if err := tx.QueryRow(ctx, `SELECT attempt FROM messages WHERE id=$1`, id).Scan(&attempt); err != nil {
			return err
		}
		assigned = true
		return insertEvent(ctx, tx, id, attempt, "assigned", "system", &l.simID, nil, map[string]any{"device_id": l.deviceID, "slot_index": l.slot}, nil)
	})
	if err != nil {
		s.Log.Error("dispatcher: assign", "err", err)
		return uuid.Nil, time.Time{}, false
	}
	return id, wait, assigned
}

func (s *Service) sendToDevice(ctx context.Context, l lane, mid uuid.UUID, eff policy.Effective) {
	m, err := scanMsg(s.Pool.QueryRow(ctx, `SELECT `+msgCols+` FROM messages WHERE id=$1`, mid))
	if err != nil || m.Body == nil {
		s.Log.Error("dispatcher: load pesan", "err", err)
		return
	}
	minGap := eff.MinDelayMs
	env, err := proto.New(proto.TypeSmsSend, proto.SmsSend{
		MessageID: mid.String(), Attempt: m.Attempt, Sim: proto.SimRef{SlotIndex: l.slot}, To: m.To, Body: *m.Body,
		DeliveryReport: eff.TrackDelivery, NotAfter: m.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"), MinGapMs: &minGap,
	}, nil)
	if err == nil {
		s.cmdRefs.Store(env.ID, mid)
		err = s.link.Send(l.deviceID, env)
	}
	if err != nil { // device menghilang antara pengecekan dan kirim: kembalikan tanpa menambah attempt
		s.cmdRefs.Delete(env.ID)
		s.Log.Warn("dispatcher: kirim ke device gagal, dikembalikan", "err", err, "message", mid)
		_ = s.withTx(ctx, func(tx pgx.Tx) error {
			c := "device_offline"
			_, e := s.transition(ctx, tx, mid, TransitionOpts{To: StatusQueued, EventType: "requeued", Actor: "system", ErrorCode: &c, SkipLane: true})
			return e
		})
	}
	s.publishMessage(m)
}

// ---------- reaper ----------

func (s *Service) RunReaper(ctx context.Context) {
	t := time.NewTicker(s.Cfg.ReaperInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.reapOnce(ctx)
		}
	}
}

func (s *Service) idsQuery(ctx context.Context, sql string, args ...any) []uuid.UUID {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		s.Log.Error("reaper query", "err", err)
		return nil
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

func (s *Service) reapOnce(ctx context.Context) {
	changed := false
	apply := func(id uuid.UUID, o TransitionOpts) {
		var m Message
		if err := s.withTx(ctx, func(tx pgx.Tx) error {
			var e error
			m, e = s.transition(ctx, tx, id, o)
			return e
		}); err != nil {
			var p *Problem
			if !errors.As(err, &p) { // transisi tak valid karena balapan = wajar
				s.Log.Warn("reaper: transisi gagal", "err", err, "message", id)
			}
			return
		}
		changed = true
		s.publishMessage(m)
	}
	// 1. lease kedaluwarsa -> queued (attempt sama; belum pernah dikonfirmasi device)
	for _, id := range s.idsQuery(ctx, `SELECT id FROM messages WHERE status='assigned' AND lease_expires_at < now() LIMIT 100`) {
		c := "ack_timeout"
		apply(id, TransitionOpts{To: StatusQueued, EventType: "requeued", Actor: "system", ErrorCode: &c, SkipLane: true})
	}
	// 2. sending terlalu lama tanpa kabar -> unknown (atau requeue bila retry_on_unknown)
	if eff, err := s.Effective(ctx, s.Pool, nil, nil, nil); err == nil {
		for _, id := range s.idsQuery(ctx, `SELECT id FROM messages WHERE status='sending' AND assigned_at < now() - ($1::int * interval '1 millisecond') LIMIT 100`, eff.SentTimeoutMs) {
			c := "sent_timeout"
			if eff.RetryOnUnknown {
				var cur int
				_ = s.Pool.QueryRow(ctx, `SELECT attempt FROM messages WHERE id=$1`, id).Scan(&cur)
				next := cur + 1
				apply(id, TransitionOpts{To: StatusQueued, EventType: "requeued", Actor: "system", ErrorCode: &c, NewAttempt: &next})
			} else {
				apply(id, TransitionOpts{To: StatusUnknown, EventType: "marked_unknown", Actor: "system", ErrorCode: &c})
			}
		}
	}
	// 3. kedaluwarsa
	for _, id := range s.idsQuery(ctx, `SELECT id FROM messages WHERE status IN ('queued','assigned') AND expires_at < now() LIMIT 200`) {
		c := "expired"
		apply(id, TransitionOpts{To: StatusExpired, EventType: "expired", Actor: "system", ErrorCode: &c, SkipLane: true})
	}
	// 4. device tanpa heartbeat
	rows, err := s.Pool.Query(ctx, `UPDATE devices SET status='offline', connected_since=NULL
		WHERE status IN ('online','degraded') AND last_seen_at < now() - ($1::bigint * interval '1 millisecond') RETURNING id`, s.Cfg.OfflineAfter.Milliseconds())
	if err == nil {
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			s.Log.Info("device offline (tanpa heartbeat)", "device", id)
			s.publishDevice(ctx, id)
		}
	}
	if changed {
		s.Wake()
	}
}

var _ = fmt.Sprintf

// ResendAssigned mengirim ulang sms.send untuk pesan 'assigned' yang belum dikenal device (setelah hello.ack).
func (s *Service) ResendAssigned(ctx context.Context, deviceID uuid.UUID, ids []uuid.UUID) {
	for _, id := range ids {
		var slot int
		var simPol, devPol *uuid.UUID
		m, err := scanMsg(s.Pool.QueryRow(ctx, `SELECT `+msgCols+` FROM messages WHERE id=$1 AND status='assigned'`, id))
		if err != nil || m.SimID == nil || m.Body == nil {
			continue
		}
		if err := s.Pool.QueryRow(ctx, `SELECT s.slot_index, s.policy_id, d.policy_id FROM sims s JOIN devices d ON d.id=s.device_id WHERE s.id=$1`, *m.SimID).Scan(&slot, &simPol, &devPol); err != nil {
			continue
		}
		eff, err := s.Effective(ctx, s.Pool, devPol, simPol, m.policyID)
		if err != nil {
			continue
		}
		s.sendToDevice(ctx, lane{simID: *m.SimID, deviceID: deviceID, slot: slot}, id, eff)
	}
}

// SendDeviceCommand: perintah jarak jauh dari dashboard (FR-03). rotate_credential belum didukung pada irisan ini.
func (s *Service) SendDeviceCommand(ctx context.Context, deviceID uuid.UUID, action string) (uuid.UUID, error) {
	switch action {
	case "ping", "refresh_sims", "restart_service", "upload_logs":
	case "rotate_credential":
		return uuid.Nil, NewProblem(http.StatusNotImplemented, "not_implemented", "Belum didukung", "rotate_credential dijadwalkan fase 3")
	default:
		return uuid.Nil, Validation(FieldError{"action", "tidak dikenal"})
	}
	if s.link == nil || !s.link.Connected(deviceID) {
		return uuid.Nil, NewProblem(http.StatusConflict, "device.offline", "Device offline", "perintah hanya dapat dikirim ke device yang online")
	}
	cid := newID()
	env, err := proto.New(proto.TypeDeviceCommand, proto.DeviceCommand{CommandID: cid.String(), Action: action}, nil)
	if err != nil {
		return uuid.Nil, err
	}
	if err := s.link.Send(deviceID, env); err != nil {
		return uuid.Nil, NewProblem(http.StatusConflict, "device.offline", "Device offline", err.Error())
	}
	return cid, nil
}
