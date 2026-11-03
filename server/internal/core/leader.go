package core

import (
	"context"
	"time"
)

const leaderLockKey = 727002

// RunLeader memilih leader lewat advisory lock PostgreSQL pada satu koneksi khusus.
// Instance yang menang memanggil lead(ctx) (menjalankan dispatcher, reaper, dst. di goroutine sendiri); ctx dibatalkan
// begitu lock hilang (koneksi putus/di-terminate, DB restart) atau server berhenti. Instance lain terus mencoba tiap tick,
// sehingga mengambil alih bila leader hilang. smsc_leader mencerminkan keadaan lock yang sebenarnya, bukan sekadar "pernah menang".
func (s *Service) RunLeader(ctx context.Context, tick time.Duration, lead func(context.Context)) {
	if tick <= 0 {
		tick = 5 * time.Second
	}
	defer s.M.Leader.Set(0)
	for ctx.Err() == nil {
		if s.leadOnce(ctx, tick, lead) {
			continue // baru kehilangan lock: coba lagi segera
		}
		select {
		case <-ctx.Done():
		case <-time.After(tick):
		}
	}
}

// leadOnce mencoba menjadi leader; true bila sempat menjadi leader lalu kehilangan lock (bukan karena shutdown).
func (s *Service) leadOnce(ctx context.Context, tick time.Duration, lead func(context.Context)) bool {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.Log.Error("leader: acquire gagal", "err", err)
		}
		return false
	}
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, leaderLockKey).Scan(&got); err != nil || !got {
		conn.Release()
		return false
	}
	lctx, cancel := context.WithCancel(ctx)
	s.Log.Info("menjadi leader: dispatcher dan reaper aktif")
	s.M.Leader.Set(1)
	s.M.LeaderChanges.WithLabelValues("acquired").Inc()
	lead(lctx)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			cancel()
			s.M.Leader.Set(0)
			_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, leaderLockKey)
			conn.Release()
			return false
		case <-t.C:
			cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
			var held bool
			err := conn.QueryRow(cctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=0 AND objid=$1 AND pid=pg_backend_pid() AND granted)`, leaderLockKey).Scan(&held)
			ccancel()
			if ctx.Err() != nil {
				continue
			}
			if err != nil || !held {
				cancel()
				s.M.Leader.Set(0)
				s.M.LeaderChanges.WithLabelValues("lost").Inc()
				s.Log.Error("leader hilang: lock advisory tidak lagi dipegang; worker dihentikan dan pemilihan diulang", "err", err)
				_ = conn.Conn().Close(context.Background()) // koneksi rusak/tidak memegang lock: buang dari pool
				conn.Release()
				return true
			}
		}
	}
}
