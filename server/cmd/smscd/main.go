// smscd: server SMSC Gateway. Perintah: serve (default) | migrate | admin create.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/jipaw/smsc-app/server/internal/api"
	"github.com/jipaw/smsc-app/server/internal/bus"
	"github.com/jipaw/smsc-app/server/internal/config"
	"github.com/jipaw/smsc-app/server/internal/core"
	"github.com/jipaw/smsc-app/server/internal/db"
	"github.com/jipaw/smsc-app/server/internal/hub"
	"github.com/jipaw/smsc-app/server/internal/pwpolicy"
)

func main() {
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("koneksi database gagal", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	switch cmd {
	case "migrate":
		if err := db.Migrate(ctx, pool, log); err != nil {
			log.Error("migrasi gagal", "err", err)
			os.Exit(1)
		}
	case "admin":
		adminCmd(ctx, args, cfg, pool, log)
	case "secrets":
		secretsCmd(ctx, args, cfg, pool, log)
	case "privacy":
		privacyCmd(ctx, args, cfg, pool, log)
	case "serve":
		serve(ctx, cfg, pool, log)
	default:
		fmt.Fprintln(os.Stderr, "perintah tidak dikenal:", cmd, "(serve | migrate | admin create|reset-2fa|reset-password | secrets reencrypt | privacy erase)")
		os.Exit(2)
	}
}

func serve(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) {
	if cfg.AutoMigrate {
		if err := db.Migrate(ctx, pool, log); err != nil {
			log.Error("migrasi gagal", "err", err)
			os.Exit(1)
		}
	}
	b := bus.New(5 * time.Minute)
	svc := core.New(pool, cfg, log, b)
	h := hub.New(svc, log)
	handler := api.New(svc, h, b, log).Router()

	// Komponen leader (dispatcher/reaper) hanya di satu instance: advisory lock (ADR, spec 02 §2).
	leaderCtx, leaderCancel := context.WithCancel(ctx)
	defer leaderCancel()
	leaderConn, err := pool.Acquire(ctx)
	if err != nil {
		log.Error("acquire", "err", err)
		os.Exit(1)
	}
	var leader bool
	_ = leaderConn.QueryRow(ctx, `SELECT pg_try_advisory_lock(727002)`).Scan(&leader)
	if leader {
		log.Info("menjadi leader: dispatcher dan reaper aktif")
		go svc.RunDispatcher(leaderCtx)
		go svc.RunReaper(leaderCtx)
		go svc.RunCampaigns(leaderCtx)
		go svc.RunWebhooks(leaderCtx)
		go svc.RunRetention(leaderCtx)
	} else {
		log.Warn("instance lain sudah menjadi leader; dispatcher/reaper tidak dijalankan di sini")
	}

	// metrik Prometheus: listener TERPISAH (default loopback); kosongkan METRICS_ADDR untuk menonaktifkan
	if cfg.MetricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.HandlerFor(svc.M.Reg, promhttp.HandlerOpts{Registry: svc.M.Reg}))
		msrv := &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := msrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("listener metrik berhenti", "err", err)
			}
		}()
		defer msrv.Close()
		log.Info("metrik tersedia", "addr", cfg.MetricsAddr+"/metrics")
	}
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("server berjalan", "addr", cfg.HTTPAddr, "public", cfg.PublicBaseURL)

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server berhenti", "err", err)
		}
	case <-ctx.Done():
		log.Info("shutdown: berhenti menerima koneksi, menutup sesi device")
		leaderCancel() // tidak ada lease baru
		h.CloseAll()   // 1012: device reconnect dengan backoff
		sctx, c := context.WithTimeout(context.Background(), 20*time.Second)
		defer c()
		_ = srv.Shutdown(sctx)
	}
	if leader {
		_, _ = leaderConn.Exec(context.Background(), `SELECT pg_advisory_unlock(727002)`)
	}
	leaderConn.Release()
}

func adminCmd(ctx context.Context, args []string, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) {
	usage := func() {
		fmt.Fprintln(os.Stderr, "pemakaian:\n  smscd admin create --email <email> [--password <pw>] [--role admin|operator|viewer]\n  smscd admin reset-2fa --email <email>          (admin kehilangan authenticator)\n  smscd admin reset-password --email <email> [--password <pw>]")
		os.Exit(2)
	}
	if len(args) == 0 {
		usage()
	}
	svc := core.New(pool, cfg, log, bus.New(time.Minute))
	switch args[0] {
	case "reset-2fa":
		fs := flag.NewFlagSet("admin reset-2fa", flag.ExitOnError)
		email := fs.String("email", "", "email")
		_ = fs.Parse(args[1:])
		id, err := svc.ResetMFAByEmail(ctx, *email)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gagal:", err)
			os.Exit(1)
		}
		svc.Audit(ctx, "cli:admin", "user.2fa_reset", "user", id.String(), nil)
		fmt.Printf("2FA %s direset dan semua sesinya dicabut; pengguna dapat mengaktifkan ulang dari Pengaturan > Akun\n", *email)
		return
	case "reset-password":
		fs := flag.NewFlagSet("admin reset-password", flag.ExitOnError)
		email := fs.String("email", "", "email")
		pw := fs.String("password", "", "password baru (kosong = acak, dicetak sekali)")
		_ = fs.Parse(args[1:])
		generated := false
		if *pw == "" {
			*pw, generated = randomPassword(), true
		}
		id, err := svc.SetPasswordByEmail(ctx, *email, *pw)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gagal:", err)
			os.Exit(1)
		}
		svc.Audit(ctx, "cli:admin", "user.password_reset", "user", id.String(), nil)
		fmt.Printf("password %s diganti dan semua sesinya dicabut\n", *email)
		if generated {
			fmt.Printf("password (tampil sekali): %s\n", *pw)
		}
		return
	case "create":
	default:
		usage()
	}
	fs := flag.NewFlagSet("admin create", flag.ExitOnError)
	email := fs.String("email", "", "email")
	pw := fs.String("password", "", "password (kosong = acak, dicetak sekali)")
	role := fs.String("role", "admin", "peran")
	_ = fs.Parse(args[1:])
	generated := false
	if *pw == "" {
		*pw, generated = randomPassword(), true
	}
	u, err := svc.CreateUser(ctx, *email, *pw, *role)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gagal:", err)
		os.Exit(1)
	}
	fmt.Printf("pengguna dibuat: %s (%s)\n", u.Email, u.Role)
	if generated {
		fmt.Printf("password (tampil sekali): %s\n", *pw)
	}
}

// randomPassword: 20 karakter acak; diulang bila kebetulan melanggar kebijakan (sangat jarang).
func randomPassword() string {
	for {
		raw := make([]byte, 15)
		_, _ = rand.Read(raw)
		if p := base64.RawURLEncoding.EncodeToString(raw); len(pwpolicy.Check(p)) == 0 {
			return p
		}
	}
}

func secretsCmd(ctx context.Context, args []string, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) {
	if len(args) == 0 || args[0] != "reencrypt" {
		fmt.Fprintln(os.Stderr, "pemakaian: smscd secrets reencrypt   (tulis ulang secret webhook & rahasia TOTP dengan WEBHOOK_ENC_KEY saat ini)")
		os.Exit(2)
	}
	svc := core.New(pool, cfg, log, bus.New(time.Minute))
	wh, tp, err := svc.ReencryptSecrets(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gagal:", err)
		os.Exit(1)
	}
	svc.Audit(ctx, "cli:secrets", "secrets.reencrypt", "system", "", map[string]any{"webhooks": wh, "totp": tp})
	fmt.Printf("selesai: %d webhook dan %d rahasia TOTP ditulis ulang dengan kunci saat ini. Kunci lama (…_PREVIOUS) kini aman dihapus.\n", wh, tp)
}

func privacyCmd(ctx context.Context, args []string, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) {
	if len(args) == 0 || args[0] != "erase" {
		fmt.Fprintln(os.Stderr, "pemakaian: smscd privacy erase --number <nomor> [--suppress]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("privacy erase", flag.ExitOnError)
	number := fs.String("number", "", "nomor yang datanya dihapus (08xx atau E.164)")
	suppress := fs.Bool("suppress", false, "tambahkan juga ke daftar blokir agar tidak dikirimi lagi")
	_ = fs.Parse(args[1:])
	svc := core.New(pool, cfg, log, bus.New(time.Minute))
	res, err := svc.EraseNumber(ctx, *number, *suppress, "cli:privacy")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gagal:", err)
		os.Exit(1)
	}
	fmt.Printf("selesai: %d pesan dianonimkan, %d penerima campaign dihapus, %d payload webhook dibersihkan, masuk daftar blokir: %v\n", res.Messages, res.Recipients, res.Deliveries, res.Suppressed)
}
