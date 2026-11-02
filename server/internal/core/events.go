package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jipaw/smsc-app/server/internal/policy"
	"github.com/jipaw/smsc-app/server/internal/proto"
)

var retryable = map[string]bool{
	"no_service": true, "radio_off": true, "generic_failure": true, "os_rate_limited": true, "sim_unavailable": true,
}

// backoff eksponensial: base*2^(attempt-1), maksimum max; os_rate_limited memakai pengali 5 (backoff panjang).
func backoff(eff policy.Effective, attempt int, code string) time.Duration {
	d := time.Duration(eff.RetryBackoffMs) * time.Millisecond
	for i := 1; i < attempt && d < time.Duration(eff.RetryBackoffMaxMs)*time.Millisecond; i++ {
		d *= 2
	}
	if code == "os_rate_limited" {
		d *= 5
	}
	if max := time.Duration(eff.RetryBackoffMaxMs) * time.Millisecond; d > max {
		d = max
	}
	return d
}

// OnSmsEvent memproses event status dari device (protocol V2-V4). Idempoten terhadap event_id.
func (s *Service) OnSmsEvent(ctx context.Context, deviceID uuid.UUID, ev proto.SmsEvent) error {
	evID, err := uuid.Parse(ev.EventID)
	mid, err2 := uuid.Parse(ev.MessageID)
	if err != nil || err2 != nil {
		return fmt.Errorf("id tidak valid")
	}
	s.M.EventIngest.WithLabelValues(ev.State).Inc()
	var out Message
	var changed bool
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		var seen bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM message_events WHERE device_event_id=$1)`, evID).Scan(&seen); err != nil {
			return err
		}
		if seen {
			return nil
		}
		cur, err := scanMsg(tx.QueryRow(ctx, `SELECT `+msgCols+` FROM messages WHERE id=$1 FOR UPDATE`, mid))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // pesan tak dikenal: diabaikan (device akan menerima drop pada hello berikutnya)
		}
		if err != nil {
			return err
		}
		out = cur
		data := map[string]any{"state": ev.State, "parts_total": ev.PartsTotal}
		if ev.PartIndex != nil {
			data["part_index"] = *ev.PartIndex
		}
		if ev.AndroidResultCode != nil {
			data["android_result_code"] = *ev.AndroidResultCode
		}
		stale := ev.Attempt != cur.Attempt
		if stale { // V3: dicatat tetapi tidak mengubah status
			data["stale"] = true
			return insertEvent(ctx, tx, mid, ev.Attempt, "stale_event", "device", cur.SimID, ev.ErrorCode, data, &evID)
		}
		do := func(to Status, typ string, o TransitionOpts) error {
			o.To, o.EventType, o.Actor, o.Data, o.DeviceEventID = to, typ, "device", data, &evID
			m, err := s.transition(ctx, tx, mid, o)
			if err != nil {
				return err
			}
			out, changed = m, true
			return nil
		}
		record := func(typ string) error {
			return insertEvent(ctx, tx, mid, ev.Attempt, typ, "device", cur.SimID, ev.ErrorCode, data, &evID)
		}
		switch ev.State {
		case "sending":
			if cur.Status == StatusAssigned {
				return do(StatusSending, "sending", TransitionOpts{})
			}
			return record("sending")
		case "part_sent", "part_delivered":
			return record(ev.State)
		case "sent":
			if cur.Status == StatusAssigned {
				if err := do(StatusSending, "sending", TransitionOpts{}); err != nil {
					return err
				}
			}
			if cur.Status == StatusAssigned || cur.Status == StatusSending || cur.Status == StatusUnknown {
				return do(StatusSent, "sent", TransitionOpts{})
			}
			return record("sent")
		case "delivered":
			if cur.Status == StatusAssigned {
				if err := do(StatusSending, "sending", TransitionOpts{}); err != nil {
					return err
				}
			}
			switch cur.Status {
			case StatusAssigned, StatusSending, StatusSent, StatusUnknown:
				return do(StatusDelivered, "delivered", TransitionOpts{})
			}
			return record("delivered")
		case "failed":
			code := "generic_failure"
			if ev.ErrorCode != nil {
				code = *ev.ErrorCode
			}
			if code == "permission_denied" {
				_, _ = tx.Exec(ctx, `UPDATE devices SET status='degraded', status_reason='permission_denied' WHERE id=$1 AND status IN ('online','degraded')`, deviceID)
			}
			if cur.Status == StatusAssigned {
				if err := do(StatusSending, "sending", TransitionOpts{}); err != nil {
					return err
				}
			}
			switch cur.Status {
			case StatusAssigned, StatusSending, StatusUnknown:
				var simPol, devPol *uuid.UUID
				if cur.SimID != nil {
					_ = tx.QueryRow(ctx, `SELECT s.policy_id, d.policy_id FROM sims s JOIN devices d ON d.id=s.device_id WHERE s.id=$1`, *cur.SimID).Scan(&simPol, &devPol)
				}
				eff, err := s.Effective(ctx, tx, devPol, simPol, cur.policyID)
				if err != nil {
					return err
				}
				if retryable[code] && cur.Attempt < cur.MaxAttempts && cur.ExpiresAt.After(time.Now()) {
					next, nb := cur.Attempt+1, time.Now().Add(backoff(eff, cur.Attempt, code))
					return do(StatusQueued, "requeued", TransitionOpts{NewAttempt: &next, NotBefore: &nb, ErrorCode: &code})
				}
				return do(StatusFailed, "failed", TransitionOpts{ErrorCode: &code})
			case StatusSent:
				if code == "delivery_failed" {
					return do(StatusFailed, "failed", TransitionOpts{ErrorCode: &code})
				}
			}
			return record("failed")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		s.publishMessage(out)
		s.Wake()
	}
	return nil
}

// OnCommandAck: jawaban atas sms.send (ref = id envelope). accepted -> sending.
func (s *Service) OnCommandAck(ctx context.Context, deviceID uuid.UUID, ref string, ack proto.CommandAck) {
	v, ok := s.cmdRefs.LoadAndDelete(ref)
	if !ok {
		return
	}
	mid := v.(uuid.UUID)
	reason := ""
	if ack.Reason != nil {
		reason = *ack.Reason
	}
	var out Message
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := scanMsg(tx.QueryRow(ctx, `SELECT `+msgCols+` FROM messages WHERE id=$1 FOR UPDATE`, mid))
		if err != nil || cur.Status != StatusAssigned {
			return err
		}
		switch {
		case ack.Accepted || reason == "duplicate" || reason == "already_sending":
			// duplicate/already_sending: device sudah memiliki job; status lanjut dari event outbox-nya
			out, err = s.transition(ctx, tx, mid, TransitionOpts{To: StatusSending, EventType: "acked", Actor: "device", Data: map[string]any{"reason": reason}})
		case reason == "expired":
			c := "expired"
			out, err = s.transition(ctx, tx, mid, TransitionOpts{To: StatusExpired, EventType: "expired", Actor: "device", ErrorCode: &c})
		case reason == "permission_denied" || reason == "invalid":
			c := reason
			if c == "invalid" {
				c = "invalid_destination"
			}
			out, err = s.transition(ctx, tx, mid, TransitionOpts{To: StatusFailed, EventType: "failed", Actor: "device", ErrorCode: &c})
			if reason == "permission_denied" {
				_, _ = tx.Exec(ctx, `UPDATE devices SET status='degraded', status_reason='permission_denied' WHERE id=$1`, deviceID)
			}
		default: // sim_unavailable, busy -> kembali ke antrean (attempt sama, belum pernah dikirim)
			nb := time.Now().Add(5 * time.Second)
			c := reason
			if c == "" {
				c = "ack_rejected"
			}
			out, err = s.transition(ctx, tx, mid, TransitionOpts{To: StatusQueued, EventType: "requeued", Actor: "device", NotBefore: &nb, ErrorCode: &c})
		}
		return err
	})
	if err != nil {
		s.Log.Warn("command.ack gagal diproses", "err", err, "message", mid)
		return
	}
	if out.ID != uuid.Nil {
		s.publishMessage(out)
		s.Wake()
	}
}

// Reconcile (hello): bandingkan inflight device dengan DB (spec 04 §6). Mengembalikan drop/keep dan pesan assigned yang perlu dikirim ulang.
func (s *Service) Reconcile(ctx context.Context, deviceID uuid.UUID, inflight []proto.Inflight) (drop, keep []string, resend []uuid.UUID, err error) {
	drop, keep = []string{}, []string{}
	type srvMsg struct {
		status  Status
		attempt int
	}
	srv := map[uuid.UUID]srvMsg{}
	rows, err := s.Pool.Query(ctx, `SELECT id,status,attempt FROM messages WHERE device_id=$1 AND status IN ('assigned','sending')`, deviceID)
	if err != nil {
		return
	}
	for rows.Next() {
		var id uuid.UUID
		var m srvMsg
		if err = rows.Scan(&id, &m.status, &m.attempt); err != nil {
			rows.Close()
			return
		}
		srv[id] = m
	}
	rows.Close()
	onDevice := map[uuid.UUID]bool{}
	for _, in := range inflight {
		id, perr := uuid.Parse(in.MessageID)
		if perr != nil {
			continue
		}
		onDevice[id] = true
		var st Status
		var att int
		qerr := s.Pool.QueryRow(ctx, `SELECT status, attempt FROM messages WHERE id=$1`, id).Scan(&st, &att)
		switch {
		case errors.Is(qerr, pgx.ErrNoRows):
			drop = append(drop, in.MessageID)
		case qerr != nil:
			err = qerr
			return
		case st == StatusCancelled || st == StatusExpired || (in.Attempt < att):
			drop = append(drop, in.MessageID)
		default:
			keep = append(keep, in.MessageID)
		}
	}
	for id, m := range srv {
		if onDevice[id] {
			continue
		}
		switch m.status {
		case StatusAssigned:
			resend = append(resend, id)
		case StatusSending:
			var out Message
			code := "device_lost"
			if terr := s.withTx(ctx, func(tx pgx.Tx) error {
				var e error
				out, e = s.transition(ctx, tx, id, TransitionOpts{To: StatusUnknown, EventType: "marked_unknown", Actor: "system", ErrorCode: &code,
					Data: map[string]any{"reason": "device tidak memiliki job ini saat hello"}})
				return e
			}); terr == nil {
				s.publishMessage(out)
			}
		}
	}
	return
}

// OnSmsEventRaw: jalur REST fallback (/device/events); memvalidasi bentuk event lalu memakai logika yang sama.
func (s *Service) OnSmsEventRaw(ctx context.Context, deviceID uuid.UUID, raw []byte) error {
	var ev proto.SmsEvent
	if err := json.Unmarshal(raw, &ev); err != nil || ev.EventID == "" || ev.MessageID == "" || ev.State == "" {
		return fmt.Errorf("event tidak valid")
	}
	return s.OnSmsEvent(ctx, deviceID, ev)
}

// EffectiveView: policy efektif (guardrail global/device/SIM + policy terpilih sebagai preferensi) untuk debugging UI.
func (s *Service) EffectiveView(ctx context.Context, policyID uuid.UUID, simID *uuid.UUID) (PolicyDTO, error) {
	if _, err := s.GetPolicy(ctx, policyID); err != nil {
		return PolicyDTO{}, err
	}
	var devPol, simPol *uuid.UUID
	if simID != nil {
		err := s.Pool.QueryRow(ctx, `SELECT s.policy_id, d.policy_id FROM sims s JOIN devices d ON d.id=s.device_id WHERE s.id=$1`, *simID).Scan(&simPol, &devPol)
		if errors.Is(err, pgx.ErrNoRows) {
			return PolicyDTO{}, NotFound("SIM")
		}
		if err != nil {
			return PolicyDTO{}, err
		}
	}
	e, err := s.Effective(ctx, s.Pool, devPol, simPol, &policyID)
	if err != nil {
		return PolicyDTO{}, Validation(FieldError{Field: "send_window", Message: err.Error()})
	}
	out := PolicyDTO{Name: "efektif", MinDelayMs: &e.MinDelayMs, MaxDelayMs: &e.MaxDelayMs, BurstSize: &e.BurstSize, BurstPauseMs: &e.BurstPauseMs,
		MaxInflightPerSim: &e.MaxInflightPerSim, MaxAttempts: &e.MaxAttempts, RetryBackoffMs: &e.RetryBackoffMs, RetryBackoffMaxMs: &e.RetryBackoffMaxMs,
		MessageTTLSeconds: &e.MessageTTLSeconds, TrackDelivery: &e.TrackDelivery, AckTimeoutMs: &e.AckTimeoutMs, SentTimeoutMs: &e.SentTimeoutMs, RetryOnUnknown: &e.RetryOnUnknown}
	out.RateLimits = &struct {
		PerMinute *int `json:"per_minute"`
		PerHour   *int `json:"per_hour"`
		PerDay    *int `json:"per_day"`
	}{nz(e.RatePerMinute), nz(e.RatePerHour), nz(e.RatePerDay)}
	if len(e.Windows) > 0 {
		w := e.Windows[0]
		out.SendWindow = &w
	}
	return out, nil
}

func nz(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}
