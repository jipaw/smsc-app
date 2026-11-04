package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jipaw/smsc-app/server/internal/config"
	"github.com/jipaw/smsc-app/server/internal/db"
)

// doctor memeriksa konfigurasi dan lingkungan sebelum/ sesudah instalasi. Kode keluar 1 bila ada FAIL.
// Tidak pernah mencetak nilai rahasia.
func doctor(ctx context.Context, out io.Writer) int {
	fails, warns := 0, 0
	say := func(level, msg string, a ...any) {
		switch level {
		case "FAIL":
			fails++
		case "WARN":
			warns++
		}
		fmt.Fprintf(out, "[%-4s] %s\n", level, fmt.Sprintf(msg, a...))
	}
	cfg, err := config.Load()
	if err != nil {
		say("FAIL", "konfigurasi: %v", err)
		fmt.Fprintf(out, "\n%d gagal, %d peringatan\n", fails, warns)
		return 1
	}
	say("OK", "konfigurasi wajib lengkap (DATABASE_URL, PUBLIC_BASE_URL, SESSION_SECRET)")

	// rahasia
	for _, k := range config.SecretEnvs {
		if cfg.SecretFromFile[k] {
			say("OK", "%s dibaca dari %s_FILE", k, k)
		}
	}
	if !cfg.SecretFromFile["SESSION_SECRET"] {
		say("INFO", "SESSION_SECRET dari variabel env; pertimbangkan SESSION_SECRET_FILE (berkas mode 0600)")
	}
	if cfg.WebhookKeyDerived {
		say("WARN", "WEBHOOK_ENC_KEY kosong: kunci enkripsi data (secret webhook, rahasia TOTP, isi email tertunda) diturunkan dari SESSION_SECRET — set WEBHOOK_ENC_KEY baru + WEBHOOK_ENC_KEY_PREVIOUS=<SESSION_SECRET lama>, lalu `smscd secrets reencrypt` (lihat INSTALL.md)")
	}

	// URL publik dan WebAuthn
	u, perr := url.Parse(cfg.PublicBaseURL)
	if perr != nil || u.Host == "" {
		say("FAIL", "PUBLIC_BASE_URL bukan URL absolut: %q", cfg.PublicBaseURL)
	} else {
		if u.Scheme != "https" && !isLocalHost(u.Hostname()) {
			say("WARN", "PUBLIC_BASE_URL memakai %s; gunakan https (tailscale serve memberi sertifikat otomatis)", u.Scheme)
		}
		if u.Scheme == "https" && !cfg.SecureCookies {
			say("WARN", "SECURE_COOKIES=false padahal PUBLIC_BASE_URL https")
		}
		if u.Scheme == "http" && cfg.SecureCookies && !isLocalHost(u.Hostname()) {
			say("FAIL", "SECURE_COOKIES=true dengan PUBLIC_BASE_URL http: browser tidak akan menyimpan cookie sesi")
		}
		if h := u.Hostname(); cfg.WebAuthnRPID != h && !strings.HasSuffix(h, "."+cfg.WebAuthnRPID) {
			say("FAIL", "WEBAUTHN_RP_ID %q tidak cocok dengan host PUBLIC_BASE_URL %q (passkey/kunci keamanan akan gagal)", cfg.WebAuthnRPID, h)
		}
	}

	// alamat bind
	if h, _, err := net.SplitHostPort(cfg.HTTPAddr); err == nil {
		if !isLoopback(h) {
			say("WARN", "HTTP_ADDR %s tidak loopback: pastikan hanya proxy (tailscale serve/nginx) yang dapat menjangkaunya dan tambahkan alamat proxy ke TRUSTED_PROXIES", cfg.HTTPAddr)
		} else {
			say("OK", "HTTP_ADDR %s (loopback; ekspos lewat tailscale serve)", cfg.HTTPAddr)
		}
	}
	if cfg.MetricsAddr != "" {
		if h, _, err := net.SplitHostPort(cfg.MetricsAddr); err == nil && !isLoopback(h) {
			say("WARN", "METRICS_ADDR %s tidak loopback: /metrics tanpa autentikasi, batasi dengan firewall/ACL", cfg.MetricsAddr)
		}
	}
	tp := []string{}
	for _, n := range cfg.TrustedProxies {
		tp = append(tp, n.String())
	}
	say("INFO", "TRUSTED_PROXIES: %s; TRUST_TAILSCALE_HEADERS=%v", strings.Join(tp, ","), cfg.TrustTailscale)

	// kontrol biaya dan data
	if len(cfg.AllowedDestPrefixes) == 0 {
		say("WARN", "ALLOWED_DESTINATION_PREFIXES kosong: semua tujuan (termasuk internasional/premium) diizinkan; contoh: ALLOWED_DESTINATION_PREFIXES=+62")
	} else {
		say("OK", "tujuan dibatasi ke awalan %s", strings.Join(cfg.AllowedDestPrefixes, ","))
	}
	if cfg.AuditRetentionDays == 0 {
		say("INFO", "AUDIT_RETENTION_DAYS=0: audit log disimpan selamanya")
	}
	if cfg.Mail.Enabled() {
		say("OK", "SMTP aktif (%s:%d, mode %s)", cfg.Mail.Host, cfg.Mail.Port, cfg.Mail.Mode)
	} else {
		say("INFO", "SMTP nonaktif: email undangan/notifikasi tidak dikirim (tautan undangan tetap dapat disalin)")
	}
	if cfg.OIDC.Enabled() {
		say("OK", "SSO OIDC aktif (%s)", cfg.OIDC.Issuer)
	}

	// database
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := db.Open(dctx, cfg.DatabaseURL)
	if err != nil {
		say("FAIL", "koneksi database: %v", err)
	} else {
		defer pool.Close()
		var ver string
		_ = pool.QueryRow(dctx, `SHOW server_version`).Scan(&ver)
		say("OK", "database tersambung (PostgreSQL %s)", ver)
		pending, perr := db.Pending(dctx, pool)
		list := strings.Join(pending, ", ")
		if len(pending) > 3 {
			list = strings.Join(pending[:2], ", ") + ", …, " + pending[len(pending)-1]
		}
		switch {
		case perr != nil:
			say("FAIL", "status migrasi: %v", perr)
		case len(pending) > 0 && cfg.AutoMigrate:
			say("INFO", "%d migrasi tertunda, diterapkan otomatis saat serve (AUTO_MIGRATE=true): %s", len(pending), list)
		case len(pending) > 0:
			say("FAIL", "%d migrasi tertunda: jalankan `smscd migrate` (%s)", len(pending), list)
		default:
			say("OK", "skema database mutakhir")
		}
		if len(pending) == 0 && perr == nil {
			var admins int
			_ = pool.QueryRow(dctx, `SELECT count(*) FROM users WHERE role='admin' AND NOT disabled`).Scan(&admins)
			if admins == 0 {
				say("WARN", "belum ada admin aktif: `smscd admin create --email <email>`")
			}
			var raw []byte
			level := "off"
			if pool.QueryRow(dctx, `SELECT value FROM settings WHERE key='security'`).Scan(&raw) == nil {
				var v struct {
					Require2FA string `json:"require_2fa"`
				}
				if json.Unmarshal(raw, &v) == nil && v.Require2FA != "" {
					level = v.Require2FA
				}
			}
			if level == "off" {
				say("INFO", "kewajiban 2FA nonaktif (default); saran: aktifkan untuk admin di Pengaturan → Keamanan")
			} else {
				say("OK", "kewajiban 2FA: %s", level)
			}
		}
	}
	fmt.Fprintf(out, "\n%d gagal, %d peringatan\n", fails, warns)
	if fails > 0 {
		return 1
	}
	return 0
}

func isLoopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func isLocalHost(h string) bool { return isLoopback(h) || strings.HasSuffix(h, ".localhost") }
