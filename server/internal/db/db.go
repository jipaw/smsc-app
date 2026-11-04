// Package db: pool PostgreSQL dan migrasi sederhana (berkas SQL tertanam, dicatat di schema_migrations).
package db

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(url, "pool_max_conns") && cfg.MaxConns < 20 {
		cfg.MaxConns = 20 // leader, listener hub, dan worker memegang koneksi tetap; default pgx (4) terlalu kecil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// Migrate menerapkan migrasi yang belum tercatat. Dilindungi advisory lock agar aman bila dua proses start bersamaan.
// Berkas boleh membawa BEGIN/COMMIT sendiri; versi dicatat setelah berkas sukses.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(727001)`); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(727001)`) //nolint:errcheck
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		var done bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)`, n).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		sqlBytes, _ := migrationFS.ReadFile("migrations/" + n)
		if _, err := conn.Exec(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("migrasi %s gagal: %w", n, err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, n); err != nil {
			return err
		}
		log.Info("migrasi diterapkan", "version", n)
	}
	return nil
}

// Pending: migrasi tertanam yang belum tercatat di schema_migrations (tanpa menerapkannya).
func Pending(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, err
	}
	done := map[string]bool{}
	if exists {
		rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v string
			_ = rows.Scan(&v)
			done[v] = true
		}
		rows.Close()
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !done[e.Name()] {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}
