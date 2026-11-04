package core

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Pengambilalihan leader tanpa kehilangan lease (fase 21).
//
// Seluruh keadaan dispatcher ada di database, tetapi ada dua celah saat leader berganti:
//  1. Pesan 'assigned' oleh leader lama yang belum sempat dikirim ke device (proses mati/lock hilang di antara penugasan dan
//     pengiriman) hanya menunggu lease habis (ack timeout) sebelum diantre ulang.
//  2. Selama tidak ada leader, tidak ada reaper; lease yang habis di jeda itu baru diproses begitu leader baru jalan, padahal
//     habisnya bukan karena device lalai membalas.
// RecoverAssigned menutup keduanya: pesan 'assigned' yang leasenya masih berlaku ATAU habis sejak leader terakhir terlihat
// diberi lease baru dan sms.send dikirim ulang (device menolak duplikat berdasarkan message_id+attempt, OnCommandAck sudah
// memperlakukan 'duplicate'/'already_sending' sebagai diterima). Dijalankan sebelum reaper mulai.

const (
	leaderSeenKey = "leader_seen"
	recoverMinAge = 2 * time.Second // pesan assigned lebih muda dari ini mungkin sedang dikirim leader lama
)

// touchLeaderSeen mencatat bahwa leader hidup (dipanggil tiap tick oleh pemegang lock).
func (s *Service) touchLeaderSeen(ctx context.Context) {
	_, _ = s.Pool.Exec(ctx, `INSERT INTO settings(key,value) VALUES ($1, to_jsonb(now())) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, leaderSeenKey)
}

// readLeaderSeen: waktu terakhir leader sebelumnya terlihat (nol bila belum pernah).
func (s *Service) readLeaderSeen(ctx context.Context) time.Time {
	var t *time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT (value #>> '{}')::timestamptz FROM settings WHERE key=$1`, leaderSeenKey).Scan(&t); err != nil || t == nil {
		return time.Time{}
	}
	return *t
}

// RecoverAssigned memulihkan pesan 'assigned' (lihat komentar paket). since = waktu leader sebelumnya terakhir terlihat
// (nol = hanya lease yang masih berlaku). skip berisi id yang sudah dipulihkan pada pengambilalihan yang sama. Mengembalikan jumlah dipulihkan.
func (s *Service) RecoverAssigned(ctx context.Context, since time.Time, skip map[uuid.UUID]bool) int {
	if s.link == nil || s.Paused(ctx) { // kill switch: jangan mengirim apa pun; reaper yang menangani lease
		return 0
	}
	if since.IsZero() {
		since = time.Now()
	}
	rows, err := s.Pool.Query(ctx, `SELECT m.id, m.device_id FROM messages m
		WHERE m.status='assigned' AND m.device_id IS NOT NULL AND m.lease_expires_at > $1
		  AND (NOT EXISTS (SELECT 1 FROM cmd_refs c WHERE c.message_id=m.id) OR m.assigned_at < now() - $2::interval)
		ORDER BY m.assigned_at, m.id`, since, recoverMinAge.String())
	if err != nil {
		s.Log.Error("recover: query", "err", err)
		return 0
	}
	byDev := map[uuid.UUID][]uuid.UUID{}
	var order []uuid.UUID
	for rows.Next() {
		var id, dev uuid.UUID
		if rows.Scan(&id, &dev) != nil || skip[id] {
			continue
		}
		if _, ok := byDev[dev]; !ok {
			order = append(order, dev)
		}
		byDev[dev] = append(byDev[dev], id)
	}
	rows.Close()
	total := 0
	for _, dev := range order {
		if ctx.Err() != nil {
			break
		}
		if !s.link.Connected(dev) { // device offline: biarkan reaper menjalankan ack_timeout
			continue
		}
		n := s.resendAssigned(ctx, dev, byDev[dev], true)
		for _, id := range byDev[dev] {
			skip[id] = true
		}
		total += n
	}
	if total > 0 {
		s.M.LeaseRecovered.Add(float64(total))
		s.Log.Info("pengambilalihan leader: pesan assigned dipulihkan", "jumlah", total)
	}
	return total
}

// LeaderWorkers: dipanggil begitu menjadi leader. Memulihkan pesan assigned lebih dulu (pass kedua setelah cache sesi
// instance lain terisi), baru menjalankan semua worker leader. Semua berhenti bersama ctx.
func (s *Service) LeaderWorkers(ctx context.Context) {
	go func() {
		since := s.prevLeaderSeen()
		skip := map[uuid.UUID]bool{}
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		s.RecoverAssigned(rctx, since, skip)
		cancel()
		if ctx.Err() != nil {
			return
		}
		go s.RunDispatcher(ctx)
		go s.RunReaper(ctx)
		go s.RunCampaigns(ctx)
		go s.RunWebhooks(ctx)
		go s.RunEmails(ctx)
		go s.RunSCIMReconcile(ctx) // pemetaan grup SCIM → peran: saat start, lalu berkala
		go s.RunRetention(ctx)
		// pass kedua: sesi device di instance lain mungkin belum terlihat saat pass pertama
		wait := 3 * s.Cfg.HubTick
		if wait <= 0 {
			wait = 3 * time.Second
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
			s.RecoverAssigned(ctx, since, skip)
		}
	}()
}

func (s *Service) prevLeaderSeen() time.Time {
	if ns := s.prevSeenNs.Load(); ns > 0 {
		return time.Unix(0, ns)
	}
	return time.Time{}
}

// ReapOnceForTest menjalankan satu putaran reaper (dipakai tes agar tidak menunggu ticker).
func (s *Service) ReapOnceForTest(ctx context.Context) { s.reapOnce(ctx) }
