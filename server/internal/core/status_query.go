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

// OnSmsState memproses jawaban device atas sms.status, termasuk pesan multi-bagian (fase 24):
//   - hitungan bagian dinormalkan dulu: jawaban `sent` hanya dipercaya bila parts_sent mencapai parts_total, `delivered` bila
//     parts_delivered mencapai parts_total (jawaban yang tidak konsisten diturunkan ke keadaan yang didukung hitungannya);
//   - kemajuan (parts_sent + parts_delivered naik sejak jawaban sebelumnya) mengembalikan satu kuota tanya, sehingga pesan panjang
//     yang terus bergerak tidak dinilai unknown hanya karena kuota habis;
//   - `failed` setelah sebagian bagian terkirim TIDAK diantre ulang (bagian itu sudah sampai ke penerima) dan dicatat parsial.
func (s *Service) OnSmsState(ctx context.Context, deviceID uuid.UUID, st proto.SmsState) {
	mid, err := uuid.Parse(st.MessageID)
	if err != nil {
		return
	}
	var dev *uuid.UUID
	var status Status
	var attempt, srvParts, lastProgress int
	if err := s.Pool.QueryRow(ctx, `SELECT device_id, status, attempt, parts, status_progress FROM messages WHERE id=$1`, mid).Scan(&dev, &status, &attempt, &srvParts, &lastProgress); err != nil {
		return
	}
	if dev == nil || *dev != deviceID || status != StatusSending || st.Attempt != attempt {
		s.M.StatusQueries.WithLabelValues("stale").Inc() // bukan lagi sending / attempt lain / device lain: abaikan
		return
	}
	total := srvParts
	if st.PartsTotal != nil {
		total = *st.PartsTotal
	}
	sent, delivered := 0, 0
	valid := total >= 1
	if st.PartsSent != nil {
		sent = *st.PartsSent
	}
	if st.PartsDelivered != nil {
		delivered = *st.PartsDelivered
	}
	if sent < 0 || delivered < 0 || sent > total || delivered > total || delivered > sent {
		valid = false // mustahil: bagian terkirim/diterima melebihi total, atau diterima tanpa terkirim
	}
	if !valid {
		s.M.StatusQueries.WithLabelValues("invalid").Inc()
		return
	}
	extra := map[string]any{"via": "sms.state", "parts_sent": sent, "parts_delivered": delivered}
	if st.PartsTotal != nil && srvParts > 0 && *st.PartsTotal != srvParts {
		extra["parts_total_server"] = srvParts // selisih hitungan bagian antara server dan device dicatat, bukan dianggap kesalahan
	}
	state := st.State
	switch state { // normalkan jawaban yang tidak didukung hitungannya
	case "delivered":
		if st.PartsDelivered != nil && delivered < total {
			state = "sending"
			if sent >= total {
				state = "sent"
			}
		}
	}
	if state == "sent" && st.PartsSent != nil && sent < total {
		state = "sending"
	}
	switch state {
	case "sent", "delivered", "failed":
		parts := total
		ev := proto.SmsEvent{EventID: uuid.NewString(), MessageID: st.MessageID, Attempt: attempt, State: state, PartsTotal: parts, ErrorCode: st.ErrorCode,
			At: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}
		o := eventOpts{Extra: extra}
		if state == "failed" {
			if ev.ErrorCode == nil {
				c := "generic_failure"
				ev.ErrorCode = &c
			}
			if sent > 0 {
				o.NoRetry = true
				o.PartialSent = &sent
				extra["partial"] = true
				s.M.StatusQueries.WithLabelValues("partial").Inc()
			}
		}
		if err := s.onSmsEvent(ctx, deviceID, ev, o); err != nil {
			s.Log.Warn("sms.state: terapkan keadaan gagal", "err", err, "message", mid)
			return
		}
		s.M.StatusQueries.WithLabelValues("settled").Inc()
	case "accepted", "sending":
		// device masih mengerjakan: beri satu sent_timeout lagi (kuota tanya membatasi total perpanjangan)
		progress := sent + delivered
		if eff, err := s.Effective(ctx, s.Pool, nil, nil, nil); err == nil {
			q := `UPDATE messages SET sent_grace_ms = sent_grace_ms + $2::int WHERE id=$1 AND status='sending' AND $3::int >= 0`
			if progress > lastProgress { // ada kemajuan: kuota tanya dikembalikan satu dan kemajuan dicatat
				q = `UPDATE messages SET sent_grace_ms = sent_grace_ms + $2::int, status_progress = $3::int,
					status_queries = GREATEST(status_queries - 1, 0) WHERE id=$1 AND status='sending'`
				_ = s.withTx(ctx, func(tx pgx.Tx) error {
					return insertEvent(ctx, tx, mid, attempt, "status_progress", "device", nil, nil, extra, nil)
				})
				s.M.StatusQueries.WithLabelValues("progress").Inc()
			}
			_, _ = s.Pool.Exec(ctx, q, mid, eff.SentTimeoutMs, progress)
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
