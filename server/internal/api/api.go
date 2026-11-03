// Package api: HTTP REST + SSE (contracts/openapi.yaml).
package api

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/jipaw/smsc-app/server/internal/bus"
	"github.com/jipaw/smsc-app/server/internal/core"
	"github.com/jipaw/smsc-app/server/internal/hub"
	"github.com/jipaw/smsc-app/server/internal/webui"
)

const cookieName = "smsc_session"

const (
	roleViewer   = 1
	roleOperator = 2
	roleAdmin    = 3
)

var roleRank = map[string]int{"viewer": roleViewer, "operator": roleOperator, "admin": roleAdmin}

type API struct {
	Svc *core.Service
	Hub *hub.Hub
	Bus *bus.Bus
	Log *slog.Logger

	loginLimit  *limiter
	enrollLimit *limiter
	keyLimit    *limiter
	userLimit   *limiter
	acctLimit   *limiter // operasi sensitif akun (ganti password, 2FA), per pengguna
}

func New(svc *core.Service, h *hub.Hub, b *bus.Bus, log *slog.Logger) *API {
	lr, lb := svc.Cfg.LoginRatePerMin, svc.Cfg.LoginBurst
	if lr <= 0 {
		lr = 5
	}
	if lb <= 0 {
		lb = 5
	}
	return &API{Svc: svc, Hub: h, Bus: b, Log: log, acctLimit: newLimiter(2*lr/60, 2*lb),
		loginLimit: newLimiter(lr/60, lb), enrollLimit: newLimiter(10.0/60, 10), keyLimit: newLimiter(20, 40), userLimit: newLimiter(10, 20)}
}

func (a *API) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer, a.securityHeaders, a.requestLog, a.httpMetrics)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		if err := a.Svc.Pool.Ping(req.Context()); err != nil {
			http.Error(w, "db tidak siap", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
	r.Get("/ws/device", a.Hub.ServeWS)
	r.Get("/enroll/{token}", func(w http.ResponseWriter, req *http.Request) { // fallback web: kode untuk input manual
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Buka aplikasi SMSC dan masukkan token ini:\n" + chi.URLParam(req, "token") + "\n"))
	})

	r.Route("/api/v1", func(r chi.Router) {
		// publik
		r.With(a.limit(a.loginLimit, clientIP)).Post("/auth/login", a.login)
		r.With(a.limit(a.loginLimit, clientIP)).Post("/auth/login/verify", a.loginVerify)
		r.With(a.limit(a.loginLimit, clientIP)).Post("/auth/login/webauthn/begin", a.webauthnLoginBegin)
		r.With(a.limit(a.loginLimit, clientIP)).Post("/auth/login/webauthn/finish", a.webauthnLoginFinish)
		r.With(a.limit(a.loginLimit, clientIP)).Post("/invitations/lookup", a.lookupInvitation)
		r.With(a.limit(a.loginLimit, clientIP)).Post("/invitations/accept", a.acceptInvitation)
		r.With(a.limit(a.enrollLimit, clientIP)).Post("/device/enroll", a.deviceEnroll)

		// device agent
		r.Group(func(r chi.Router) {
			r.Use(a.deviceAuth)
			r.Get("/device/config", a.deviceConfig)
			r.Post("/device/events", a.deviceEvents)
		})

		// dashboard / API key
		r.Group(func(r chi.Router) {
			r.Use(a.authenticate, a.csrf, a.mfaGate)
			r.Post("/auth/logout", a.logout)
			r.Get("/auth/me", a.me)
			r.With(a.limit(a.acctLimit, userKey)).Post("/auth/password", a.changePassword)
			r.Get("/auth/2fa", a.mfaStatus)
			r.Get("/auth/webauthn", a.webauthnList)
			r.With(a.limit(a.acctLimit, userKey)).Post("/auth/webauthn/register/begin", a.webauthnRegisterBegin)
			r.With(a.limit(a.acctLimit, userKey)).Post("/auth/webauthn/register/finish", a.webauthnRegisterFinish)
			r.With(a.limit(a.acctLimit, userKey)).Delete("/auth/webauthn/credentials/{credentialId}", a.webauthnRemove)
			r.With(a.limit(a.acctLimit, userKey)).Post("/auth/2fa/setup", a.mfaSetup)
			r.With(a.limit(a.acctLimit, userKey)).Post("/auth/2fa/enable", a.mfaEnable)
			r.With(a.limit(a.acctLimit, userKey)).Post("/auth/2fa/disable", a.mfaDisable)
			r.With(a.limit(a.acctLimit, userKey)).Post("/auth/2fa/recovery-codes", a.mfaRecoveryCodes)

			viewer := a.authz(roleViewer, "")
			operator := func(scope string) func(http.Handler) http.Handler { return a.authz(roleOperator, scope) }
			admin := a.authz(roleAdmin, "")

			r.With(viewer).Get("/devices", a.listDevices)
			r.With(viewer).Get("/devices/{deviceId}", a.getDevice)
			r.With(admin).Patch("/devices/{deviceId}", a.patchDevice)
			r.With(admin).Delete("/devices/{deviceId}", a.revokeDevice)
			r.With(admin).Post("/devices/{deviceId}/commands", a.deviceCommand)
			r.With(admin).Post("/device-enrollments", a.createEnrollment)
			r.With(admin).Delete("/device-enrollments/{enrollmentId}", a.deleteEnrollment)

			r.With(viewer).Get("/sims", a.listSims)
			r.With(viewer).Get("/sims/{simId}", a.getSim)
			r.With(admin).Patch("/sims/{simId}", a.patchSim)

			r.With(a.authz(roleViewer, "messages:read")).Get("/messages", a.listMessages)
			r.With(operator("messages:send")).Post("/messages", a.createMessages)
			r.With(operator("messages:read")).Get("/messages/export", a.exportMessages)
			r.With(a.authz(roleViewer, "messages:read")).Get("/messages/{messageId}", a.getMessage)
			r.With(operator("")).Post("/messages/{messageId}/cancel", a.cancelMessage)
			r.With(operator("")).Post("/messages/{messageId}/retry", a.retryMessage)

			r.With(viewer).Get("/templates", a.listTemplates)
			r.With(operator("")).Post("/templates", a.saveTemplate)
			r.With(viewer).Get("/templates/{templateId}", a.getTemplate)
			r.With(operator("")).Put("/templates/{templateId}", a.saveTemplate)
			r.With(operator("")).Delete("/templates/{templateId}", a.deleteTemplate)

			r.With(viewer).Get("/dispatch-policies", a.listPolicies)
			r.With(admin).Post("/dispatch-policies", a.savePolicy)
			r.With(viewer).Get("/dispatch-policies/{policyId}", a.getPolicy)
			r.With(admin).Put("/dispatch-policies/{policyId}", a.savePolicy)
			r.With(admin).Delete("/dispatch-policies/{policyId}", a.deletePolicy)
			r.With(viewer).Get("/dispatch-policies/{policyId}/effective", a.effectivePolicy)
			r.With(admin).Put("/settings/default-policy", a.setDefaultPolicy)
			r.With(viewer).Get("/settings/dispatch-pause", a.getPause)
			r.With(admin).Put("/settings/dispatch-pause", a.setPause)

			r.With(admin).Get("/suppressions", a.listSuppressions)
			r.With(admin).Post("/suppressions", a.addSuppressions)
			r.With(admin).Delete("/suppressions/{suppressionId}", a.deleteSuppression)
			r.With(admin).Get("/api-keys", a.listAPIKeys)
			r.With(admin).Post("/api-keys", a.createAPIKey)
			r.With(admin).Delete("/api-keys/{apiKeyId}", a.deleteAPIKey)
			r.With(admin).Post("/api-keys/{apiKeyId}/rotate", a.rotateAPIKey)
			r.With(admin).Get("/users", a.listUsers)
			r.With(admin).Post("/users", a.createUser)
			r.With(admin).Delete("/users/{userId}/2fa", a.resetUserMFA)
			r.With(admin).Patch("/users/{userId}", a.patchUser)
			r.With(admin).Delete("/users/{userId}", a.deleteUser)
			r.With(admin).Get("/invitations", a.listInvitations)
			r.With(admin).Post("/invitations", a.createInvitation)
			r.With(admin).Delete("/invitations/{invitationId}", a.revokeInvitation)
			r.With(admin).Post("/users/{userId}/password", a.setUserPassword)
			r.With(admin).Get("/settings/email", a.getEmailConfig)
			r.With(admin).With(a.limit(a.acctLimit, userKey)).Post("/settings/email/test", a.testEmail)
			r.With(admin).Get("/settings/security", a.getSecurity)
			r.With(admin).Put("/settings/security", a.setSecurity)
			r.With(admin).Get("/audit-logs", a.listAudit)

			r.With(viewer).Get("/stats/overview", a.overview)
			r.With(viewer).Get("/stats/timeseries", a.timeseries)
			r.With(viewer).Get("/events/stream", a.stream)

			// campaign (fase 2)
			r.With(viewer).Get("/campaigns", a.listCampaigns)
			r.With(operator("")).Post("/campaigns", a.createCampaign)
			r.With(viewer).Get("/campaigns/{campaignId}", a.getCampaign)
			r.With(operator("")).Patch("/campaigns/{campaignId}", a.patchCampaign)
			r.With(operator("")).Delete("/campaigns/{campaignId}", a.deleteCampaign)
			r.With(operator("")).Post("/campaigns/{campaignId}/recipients", a.importRecipients)
			r.With(operator("")).Delete("/campaigns/{campaignId}/recipients", a.clearRecipients)
			r.With(operator("")).Post("/campaigns/{campaignId}/preview", a.previewCampaign)
			for _, act := range []string{"start", "pause", "resume", "cancel"} {
				r.With(operator("")).Post("/campaigns/{campaignId}/"+act, a.campaignAction(act))
			}

			// webhook & retensi (fase 3)
			r.With(admin).Get("/webhooks", a.listWebhooks)
			r.With(admin).Post("/webhooks", a.createWebhook)
			r.With(admin).Get("/webhooks/{webhookId}", a.getWebhook)
			r.With(admin).Patch("/webhooks/{webhookId}", a.patchWebhook)
			r.With(admin).Delete("/webhooks/{webhookId}", a.deleteWebhook)
			r.With(admin).Post("/webhooks/{webhookId}/test", a.testWebhook)
			r.With(admin).Post("/webhooks/{webhookId}/rotate-secret", a.rotateWebhookSecret)
			r.With(admin).Get("/webhooks/{webhookId}/deliveries", a.listDeliveries)
			r.With(admin).Post("/webhooks/{webhookId}/deliveries/{deliveryId}/retry", a.retryDelivery)
			r.With(admin).Get("/settings/retention", a.getRetention)
			r.With(admin).Put("/settings/retention", a.putRetention)
		})
	})

	r.Handle("/*", webui.Handler())
	return r
}

func (a *API) notImplemented(w http.ResponseWriter, r *http.Request) {
	a.writeProblem(w, r, core.NewProblem(http.StatusNotImplemented, "not_implemented", "Belum diimplementasikan", "fitur ini dijadwalkan di fase 2/3 (lihat docs/05)"))
}

func (a *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; img-src 'self' data:; style-src 'self' 'unsafe-inline'")
		if a.Svc.Cfg.SecureCookies {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		w.Header().Set("X-Request-Id", middleware.GetReqID(r.Context()))
		next.ServeHTTP(ww, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws/device" {
			// tidak pernah mencatat body atau query yang berisi nomor
			a.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "ms", time.Since(start).Milliseconds(), "rid", middleware.GetReqID(r.Context()))
		}
	})
}

// ---------- autentikasi & otorisasi ----------

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var p *principal
		if tok := bearer(r); strings.HasPrefix(tok, "smsc_live_") {
			id, scopes, err := a.Svc.AuthAPIKey(ctx, tok)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			if !a.keyLimit.allow(id.String()) {
				w.Header().Set("Retry-After", "1")
				a.writeProblem(w, r, core.NewProblem(http.StatusTooManyRequests, "rate_limited", "Terlalu banyak permintaan", ""))
				return
			}
			p = &principal{apiKeyID: id, scopes: scopes}
		} else if c, err := r.Cookie(cookieName); err == nil {
			u, csrf, err := a.Svc.SessionUser(ctx, c.Value)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			if !a.userLimit.allow(u.ID.String()) {
				w.Header().Set("Retry-After", "1")
				a.writeProblem(w, r, core.NewProblem(http.StatusTooManyRequests, "rate_limited", "Terlalu banyak permintaan", ""))
				return
			}
			p = &principal{user: &u, csrf: csrf}
		} else {
			a.writeProblem(w, r, core.NewProblem(http.StatusUnauthorized, "auth.unauthenticated", "Tidak terautentikasi", "login diperlukan"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, keyPrincipal, p)))
	})
}

// mfaGate: bila kebijakan mewajibkan 2FA dan pengguna belum memilikinya, hanya endpoint akun yang terbuka.
// API key tidak terpengaruh (bukan pengguna).
var mfaSetupPaths = map[string]bool{
	"/api/v1/auth/me": true, "/api/v1/auth/logout": true, "/api/v1/auth/password": true,
	"/api/v1/auth/2fa": true, "/api/v1/auth/2fa/setup": true, "/api/v1/auth/2fa/enable": true,
	"/api/v1/auth/webauthn": true, "/api/v1/auth/webauthn/register/begin": true, "/api/v1/auth/webauthn/register/finish": true,
}

func (a *API) mfaGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := principalOf(r.Context()); p != nil && !p.isKey() && !mfaSetupPaths[r.URL.Path] && a.Svc.MFASetupRequired(r.Context(), *p.user) {
			a.writeProblem(w, r, core.NewProblem(http.StatusForbidden, "auth.mfa_setup_required", "2FA wajib diaktifkan",
				"kebijakan keamanan mewajibkan 2FA untuk akun Anda; aktifkan di halaman Akun"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrf: mutasi dengan sesi wajib menyertakan X-CSRF-Token (API key tidak memakai cookie).
func (a *API) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalOf(r.Context())
		if p != nil && !p.isKey() && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if r.Header.Get("X-CSRF-Token") != p.csrf {
				a.writeProblem(w, r, core.NewProblem(http.StatusForbidden, "auth.csrf", "Token CSRF salah", ""))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// authz: pengguna sesi dengan peran >= minRole, atau API key dengan scope tertentu (scope "" = API key ditolak).
func (a *API) authz(minRole int, scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := principalOf(r.Context())
			ok := false
			switch {
			case p == nil:
			case p.isKey():
				for _, s := range p.scopes {
					if scope != "" && s == scope {
						ok = true
					}
				}
			default:
				ok = roleRank[p.user.Role] >= minRole
			}
			if !ok {
				a.writeProblem(w, r, core.NewProblem(http.StatusForbidden, "auth.forbidden", "Akses ditolak", "peran atau scope tidak cukup"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (a *API) deviceAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" {
			a.writeProblem(w, r, core.NewProblem(http.StatusUnauthorized, "auth.unauthenticated", "Tidak terautentikasi", ""))
			return
		}
		id, err := a.Svc.AuthDevice(r.Context(), tok)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyDevice, id)))
	})
}

func actorOf(p *principal) string {
	if p == nil {
		return "system"
	}
	if p.isKey() {
		return "apikey:" + p.apiKeyID.String()
	}
	return "user:" + p.user.Email
}

func ownerOf(p *principal) uuid.UUID {
	if p.isKey() {
		return p.apiKeyID
	}
	return p.user.ID
}

// httpMetrics mencatat permintaan dengan POLA rute (mis. /api/v1/messages/{messageId}), bukan URL, agar kardinalitas tetap kecil.
func (a *API) httpMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		if r.URL.Path == "/api/v1/events/stream" || r.URL.Path == "/ws/device" { // koneksi panjang: tidak masuk histogram durasi
			a.Svc.M.HTTPRequests.WithLabelValues(r.Method, route, strconv.Itoa(ww.Status())).Inc()
			return
		}
		st := ww.Status()
		if st == 0 {
			st = 200
		}
		a.Svc.M.HTTPRequests.WithLabelValues(r.Method, route, strconv.Itoa(st)).Inc()
		a.Svc.M.HTTPDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}
