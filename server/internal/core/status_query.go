package core

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jipaw/smsc-app/server/internal/proto"
)

// Fase 23: sebelum pesan 'sending' yang melewati sent_timeout dinilai unknown / diantre ulang, tanya device (sms.status).
// Jawaban `sms.state` diproses OnSmsState. Paling banyak maxStatusQueries per percobaan kirim; device lama yang tidak
// menjawab (balasan error unknown_type atau diam) membuat alur lama berlaku setelah batas tunggu.
const maxStatusQueries = 2

// askDeviceStatus mengirim sms.status bila device masih tersambung dan kuota tanya belum habis. true = keputusan ditunda.
func (s *Service) askDeviceStatus(ctx context.Context, id uuid.UUID) bool {
	if s.link == nil {
		return false
	}
	var dev *uuid.UUID
	var attempt, queried int
	if err := s.Pool.QueryRow(ctx, `SELECT device_id, attempt, status_queries FROM messages WHERE id=$1 AND status='sending'`, id).Scan(&dev, &attempt, &queried); err != nil {
		return false
	}
	if dev == nil || queried >= maxStatusQueries || !s.link.Connected(*dev) {
		return false
	}
	env, err := proto.New(proto.TypeSmsStatus, proto.SmsStatus{MessageID: id.String(), Attempt: attempt}, nil)
	if err != nil || s.link.Send(*dev, env) != nil {
		return false
	}
	wait := s.Cfg.StatusQueryWait
	if wait <= 0 {
		wait = 15 * time.Second
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE messages SET status_queries = status_queries + 1, sent_grace_ms = sent_grace_ms + $2::int WHERE id=$1 AND status='sending'`, id, int(wait.Milliseconds())); err != nil {
		return false
	}
	s.M.StatusQueries.WithLabelValues("queried").Inc()
	return true
}

// OnSmsState memproses jawaban device atas sms.status.
func (s *Service) OnSmsState(ctx context.Context, deviceID uuid.UUID, st proto.SmsState) {
	mid, err := uuid.Parse(st.MessageID)
	if err != nil {
		return
	}
	var dev *uuid.UUID
	var status Status
	var attempt int
	if err := s.Pool.QueryRow(ctx, `SELECT device_id, status, attempt FROM messages WHERE id=$1`, mid).Scan(&dev, &status, &attempt); err != nil {
		return
	}
	if dev == nil || *dev != deviceID || status != StatusSending || st.Attempt != attempt {
		s.M.StatusQueries.WithLabelValues("stale").Inc() // bukan lagi sending / attempt lain / device lain: abaikan
		return
	}
	switch st.State {
	case "sent", "delivered", "failed":
		parts := 1
		if st.PartsTotal != nil && *st.PartsTotal > 0 {
			parts = *st.PartsTotal
		}
		ev := proto.SmsEvent{EventID: uuid.NewString(), MessageID: st.MessageID, Attempt: attempt, State: st.State, PartsTotal: parts, ErrorCode: st.ErrorCode,
			At: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}
		if st.State == "failed" && ev.ErrorCode == nil {
			c := "generic_failure"
			ev.ErrorCode = &c
		}
		if err := s.OnSmsEvent(ctx, deviceID, ev); err != nil {
			s.Log.Warn("sms.state: terapkan keadaan gagal", "err", err, "message", mid)
			return
		}
		s.M.StatusQueries.WithLabelValues("settled").Inc()
	case "accepted", "sending":
		// device masih mengerjakan: beri satu sent_timeout lagi (kuota tanya membatasi total perpanjangan)
		if eff, err := s.Effective(ctx, s.Pool, nil, nil, nil); err == nil {
			_, _ = s.Pool.Exec(ctx, `UPDATE messages SET sent_grace_ms = sent_grace_ms + $2::int WHERE id=$1 AND status='sending'`, mid, eff.SentTimeoutMs)
		}
		s.M.StatusQueries.WithLabelValues("alive").Inc()
	case "unknown":
		code := "device_lost"
		var out Message
		if err := s.withTx(ctx, func(tx pgx.Tx) error {
			var e error
			out, e = s.transition(ctx, tx, mid, TransitionOpts{To: StatusUnknown, EventType: "marked_unknown", Actor: "system", ErrorCode: &code,
				Data: map[string]any{"reason": "device menjawab sms.state: tidak memiliki job ini"}})
			return e
		}); err != nil {
			return
		}
		s.publishMessage(out)
		s.Wake()
		s.M.StatusQueries.WithLabelValues("lost").Inc()
	}
}
