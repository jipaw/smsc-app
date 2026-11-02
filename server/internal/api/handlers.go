package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jipaw/smsc-app/server/internal/bus"
	"github.com/jipaw/smsc-app/server/internal/core"
)

// ---------- auth ----------

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password string }
	if !a.decode(w, r, &in) {
		return
	}
	tok, csrf, u, err := a.Svc.Login(r.Context(), in.Email, in.Password)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true, Secure: a.Svc.Cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600})
	a.Svc.Audit(r.Context(), "user:"+u.Email, "auth.login", "user", u.ID.String(), nil)
	a.writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "email": u.Email, "role": u.Role, "tailscale_login": u.TailscaleLogin, "csrf_token": csrf})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		a.Svc.Logout(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r.Context())
	if p.isKey() {
		a.writeProblem(w, r, core.NewProblem(http.StatusForbidden, "auth.forbidden", "Akses ditolak", "endpoint untuk sesi dashboard"))
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"id": p.user.ID, "email": p.user.Email, "role": p.user.Role, "tailscale_login": p.user.TailscaleLogin, "csrf_token": p.csrf})
}

// ---------- devices ----------

func (a *API) listDevices(w http.ResponseWriter, r *http.Request) {
	d, err := a.Svc.ListDevices(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": d, "next_cursor": nil})
}

func (a *API) getDevice(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "deviceId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	d, err := a.Svc.GetDevice(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, d)
}

func (a *API) patchDevice(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "deviceId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	m, ok := a.decodeMap(w, r)
	if !ok {
		return
	}
	var p core.DevicePatch
	if v, ok := m["name"]; ok {
		_ = json.Unmarshal(v, &p.Name)
	}
	if v, ok := m["enabled"]; ok {
		_ = json.Unmarshal(v, &p.Enabled)
	}
	if v, ok := m["tags"]; ok {
		_ = json.Unmarshal(v, &p.Tags)
	}
	if v, ok := m["policy_id"]; ok {
		p.PolicySet = true
		_ = json.Unmarshal(v, &p.PolicyID)
	}
	d, err := a.Svc.PatchDevice(r.Context(), id, p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(principalOf(r.Context())), "device.update", "device", id.String(), nil)
	a.writeJSON(w, http.StatusOK, d)
}

func (a *API) revokeDevice(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "deviceId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	if err := a.Svc.RevokeDevice(r.Context(), id); err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(principalOf(r.Context())), "device.revoke", "device", id.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) deviceCommand(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "deviceId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	var in struct{ Action string }
	if !a.decode(w, r, &in) {
		return
	}
	cid, err := a.Svc.SendDeviceCommand(r.Context(), id, in.Action)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(principalOf(r.Context())), "device.command", "device", id.String(), map[string]any{"action": in.Action})
	a.writeJSON(w, http.StatusAccepted, map[string]any{"command_id": cid})
}

func (a *API) createEnrollment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceName string     `json:"device_name"`
		PolicyID   *uuid.UUID `json:"policy_id"`
		TTLSeconds int        `json:"ttl_seconds"`
	}
	if !a.decode(w, r, &in) {
		return
	}
	if in.TTLSeconds != 0 && (in.TTLSeconds < 60 || in.TTLSeconds > 3600) {
		a.writeProblem(w, r, core.Validation(core.FieldError{Field: "ttl_seconds", Message: "60..3600"}))
		return
	}
	e, err := a.Svc.CreateEnrollment(r.Context(), principalOf(r.Context()).user.ID, in.DeviceName, in.PolicyID, time.Duration(in.TTLSeconds)*time.Second)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(principalOf(r.Context())), "device.enrollment.create", "enrollment", e.ID.String(), nil)
	a.writeJSON(w, http.StatusCreated, e)
}

func (a *API) deleteEnrollment(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "enrollmentId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	if err := a.Svc.RevokeEnrollment(r.Context(), id); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- device agent ----------

func (a *API) deviceEnroll(w http.ResponseWriter, r *http.Request) {
	var in core.EnrollRequest
	if !a.decode(w, r, &in) {
		return
	}
	resp, err := a.Svc.Enroll(r.Context(), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), "device:"+resp.DeviceID.String(), "device.enroll", "device", resp.DeviceID.String(), nil)
	a.writeJSON(w, http.StatusCreated, resp)
}

func (a *API) deviceConfig(w http.ResponseWriter, r *http.Request) {
	id := r.Context().Value(keyDevice).(uuid.UUID)
	c, err := a.Svc.DeviceConfigFor(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, c)
}

func (a *API) deviceEvents(w http.ResponseWriter, r *http.Request) {
	id := r.Context().Value(keyDevice).(uuid.UUID)
	var in struct {
		Events []struct {
			EventID string `json:"event_id"`
		} `json:"events"`
	}
	raw := json.RawMessage{}
	if !a.decode(w, r, &raw) {
		return
	}
	var full struct {
		Events []json.RawMessage `json:"events"`
	}
	if json.Unmarshal(raw, &full) != nil || len(full.Events) > 500 {
		a.writeProblem(w, r, core.Validation(core.FieldError{Field: "events", Message: "maksimal 500"}))
		return
	}
	_ = in
	acked := []string{}
	for _, e := range full.Events {
		var ev struct {
			EventID string `json:"event_id"`
		}
		_ = json.Unmarshal(e, &ev)
		if err := a.Svc.OnSmsEventRaw(r.Context(), id, e); err != nil {
			a.Log.Warn("device/events: event ditolak", "err", err, "event", ev.EventID)
			continue
		}
		acked = append(acked, ev.EventID)
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"acked": acked})
}

// ---------- SIM ----------

func (a *API) listSims(w http.ResponseWriter, r *http.Request) {
	dev, perr := qUUID(r, "device_id")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	s, err := a.Svc.ListSims(r.Context(), dev, r.URL.Query().Get("status"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": s})
}

func (a *API) getSim(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "simId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	s, err := a.Svc.GetSim(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, s)
}

func (a *API) patchSim(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "simId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	m, ok := a.decodeMap(w, r)
	if !ok {
		return
	}
	var p core.SimPatch
	if v, ok := m["label"]; ok {
		_ = json.Unmarshal(v, &p.Label)
	}
	if v, ok := m["enabled"]; ok {
		_ = json.Unmarshal(v, &p.Enabled)
	}
	if v, ok := m["policy_id"]; ok {
		p.PolicySet = true
		_ = json.Unmarshal(v, &p.PolicyID)
	}
	s, err := a.Svc.PatchSim(r.Context(), id, p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(principalOf(r.Context())), "sim.update", "sim", id.String(), nil)
	a.writeJSON(w, http.StatusOK, s)
}

// ---------- pesan ----------

func (a *API) maskFor(r *http.Request, ms []core.Message) {
	if p := principalOf(r.Context()); p != nil && !p.isKey() && p.user.Role == "viewer" {
		for i := range ms {
			ms[i].To = maskNumber(ms[i].To)
		}
	}
}

func parseFilter(r *http.Request) (core.MessageFilter, *core.Problem) {
	q := r.URL.Query()
	f := core.MessageFilter{Source: q.Get("source"), To: q.Get("to"), ClientRef: q.Get("client_ref"), CreatedFrom: qTime(r, "created_from"), CreatedTo: qTime(r, "created_to")}
	if v := q.Get("status"); v != "" {
		f.Statuses = strings.Split(v, ",")
	}
	var perr *core.Problem
	if f.CampaignID, perr = qUUID(r, "campaign_id"); perr != nil {
		return f, perr
	}
	if f.DeviceID, perr = qUUID(r, "device_id"); perr != nil {
		return f, perr
	}
	f.SimID, perr = qUUID(r, "sim_id")
	return f, perr
}

func (a *API) listMessages(w http.ResponseWriter, r *http.Request) {
	f, perr := parseFilter(r)
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	ms, next, err := a.Svc.ListMessages(r.Context(), f, qInt(r, "limit", 50), r.URL.Query().Get("cursor"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.maskFor(r, ms)
	var nc any
	if next != "" {
		nc = next
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": ms, "next_cursor": nc})
}

func (a *API) createMessages(w http.ResponseWriter, r *http.Request) {
	var in core.MessageCreate
	if !a.decode(w, r, &in) {
		return
	}
	p := principalOf(r.Context())
	source := "dashboard"
	if p.isKey() {
		source = "api"
	}
	res, err := a.Svc.CreateMessages(r.Context(), ownerOf(p), source, r.Header.Get("Idempotency-Key"), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusAccepted, res)
}

func (a *API) getMessage(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "messageId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	m, evs, err := a.Svc.GetMessage(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ms := []core.Message{m}
	a.maskFor(r, ms)
	b, _ := json.Marshal(ms[0])
	out := map[string]json.RawMessage{}
	_ = json.Unmarshal(b, &out)
	eb, _ := json.Marshal(evs)
	out["events"] = eb
	a.writeJSON(w, http.StatusOK, out)
}

func (a *API) cancelMessage(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "messageId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	m, err := a.Svc.CancelMessage(r.Context(), id, actorOf(principalOf(r.Context())))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, m)
}

func (a *API) retryMessage(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "messageId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	var in core.RetryRequest
	if !a.decode(w, r, &in) {
		return
	}
	m, err := a.Svc.RetryMessage(r.Context(), id, actorOf(principalOf(r.Context())), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(principalOf(r.Context())), "message.retry", "message", id.String(), map[string]any{"confirm_duplicate_risk": in.ConfirmDuplicateRisk})
	a.writeJSON(w, http.StatusOK, m)
}

func (a *API) exportMessages(w http.ResponseWriter, r *http.Request) {
	f, perr := parseFilter(r)
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="transaksi.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "created_at", "source", "to", "body", "status", "attempt", "error_code", "device_id", "sim_id", "sent_at", "delivered_at"})
	cursor := ""
	p := principalOf(r.Context())
	for pages := 0; pages < 1000; pages++ {
		ms, next, err := a.Svc.ListMessages(r.Context(), f, 200, cursor)
		if err != nil {
			a.Log.Error("export gagal", "err", err)
			break
		}
		for _, m := range ms {
			to, body := m.To, ""
			if m.Body != nil {
				body = *m.Body
			}
			if !p.isKey() && p.user.Role == "viewer" {
				to = maskNumber(to)
			}
			_ = cw.Write([]string{m.ID.String(), m.CreatedAt.UTC().Format(time.RFC3339), m.Source, csvSafe(to), csvSafe(body), string(m.Status), fmt.Sprint(m.Attempt),
				csvSafe(derefS(m.ErrorCode)), derefU(m.DeviceID), derefU(m.SimID), tstr(m.SentAt), tstr(m.DeliveredAt)})
		}
		if next == "" {
			break
		}
		cursor = next
	}
	cw.Flush()
}

func derefS(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func derefU(u *uuid.UUID) string {
	if u == nil {
		return ""
	}
	return u.String()
}
func tstr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ---------- template ----------

func (a *API) listTemplates(w http.ResponseWriter, r *http.Request) {
	t, err := a.Svc.ListTemplates(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": t})
}

func (a *API) getTemplate(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "templateId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	t, err := a.Svc.GetTemplate(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, t)
}

func (a *API) saveTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Body string }
	if !a.decode(w, r, &in) {
		return
	}
	var idp *uuid.UUID
	status := http.StatusCreated
	if r.Method == http.MethodPut {
		id, perr := pathUUID(r, "templateId")
		if perr != nil {
			a.writeProblem(w, r, perr)
			return
		}
		idp, status = &id, http.StatusOK
	}
	t, err := a.Svc.SaveTemplate(r.Context(), idp, in.Name, in.Body)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, status, t)
}

func (a *API) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "templateId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	if err := a.Svc.DeleteTemplate(r.Context(), id); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- policy ----------

func (a *API) listPolicies(w http.ResponseWriter, r *http.Request) {
	p, err := a.Svc.ListPolicies(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": p})
}

func (a *API) getPolicy(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "policyId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	p, err := a.Svc.GetPolicy(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, p)
}

func (a *API) savePolicy(w http.ResponseWriter, r *http.Request) {
	var in core.PolicyDTO
	if !a.decode(w, r, &in) {
		return
	}
	var idp *uuid.UUID
	status := http.StatusCreated
	if r.Method == http.MethodPut {
		id, perr := pathUUID(r, "policyId")
		if perr != nil {
			a.writeProblem(w, r, perr)
			return
		}
		idp, status = &id, http.StatusOK
	}
	p, err := a.Svc.SavePolicy(r.Context(), idp, in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(principalOf(r.Context())), "policy.save", "policy", p.ID.String(), nil)
	a.writeJSON(w, status, p)
}

func (a *API) deletePolicy(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "policyId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	if err := a.Svc.DeletePolicy(r.Context(), id); err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(principalOf(r.Context())), "policy.delete", "policy", id.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) effectivePolicy(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "policyId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	sim, perr := qUUID(r, "sim_id")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	out, err := a.Svc.EffectiveView(r.Context(), id, sim)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, out)
}

func (a *API) setDefaultPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PolicyID uuid.UUID `json:"policy_id"`
	}
	if !a.decode(w, r, &in) {
		return
	}
	if err := a.Svc.SetDefaultPolicy(r.Context(), in.PolicyID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(principalOf(r.Context())), "policy.set_default", "policy", in.PolicyID.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) getPause(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, map[string]any{"paused": a.Svc.Paused(r.Context())})
}

func (a *API) setPause(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paused bool `json:"paused"`
	}
	if !a.decode(w, r, &in) {
		return
	}
	if err := a.Svc.SetPaused(r.Context(), in.Paused, actorOf(principalOf(r.Context()))); err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"paused": in.Paused})
}

// ---------- admin ----------

func (a *API) listSuppressions(w http.ResponseWriter, r *http.Request) {
	items, next, err := a.Svc.ListSuppressions(r.Context(), qInt(r, "limit", 50), r.URL.Query().Get("cursor"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var nc any
	if next != "" {
		nc = next
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nc})
}

func (a *API) addSuppressions(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Numbers []string `json:"numbers"`
		Reason  string   `json:"reason"`
	}
	if !a.decode(w, r, &in) {
		return
	}
	added, invalid, err := a.Svc.AddSuppressions(r.Context(), principalOf(r.Context()).user.ID, in.Numbers, in.Reason)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, map[string]any{"added": added, "invalid": invalid})
}

func (a *API) deleteSuppression(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "suppressionId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	if err := a.Svc.DeleteSuppression(r.Context(), id); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	k, err := a.Svc.ListAPIKeys(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": k})
}

func (a *API) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string     `json:"name"`
		Scopes    []string   `json:"scopes"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if !a.decode(w, r, &in) {
		return
	}
	k, err := a.Svc.CreateAPIKey(r.Context(), principalOf(r.Context()).user.ID, in.Name, in.Scopes, in.ExpiresAt)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(principalOf(r.Context())), "apikey.create", "apikey", k.ID.String(), map[string]any{"name": k.Name})
	a.writeJSON(w, http.StatusCreated, k)
}

func (a *API) deleteAPIKey(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "apiKeyId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	if err := a.Svc.RevokeAPIKey(r.Context(), id); err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(principalOf(r.Context())), "apikey.revoke", "apikey", id.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listUsers(w http.ResponseWriter, r *http.Request) {
	u, err := a.Svc.ListUsers(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": u})
}

func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Role, Password string }
	if !a.decode(w, r, &in) {
		return
	}
	u, err := a.Svc.CreateUser(r.Context(), in.Email, in.Password, in.Role)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(principalOf(r.Context())), "user.create", "user", u.ID.String(), map[string]any{"role": u.Role})
	a.writeJSON(w, http.StatusCreated, u)
}

func (a *API) listAudit(w http.ResponseWriter, r *http.Request) {
	items, next, err := a.Svc.ListAudit(r.Context(), qInt(r, "limit", 50), r.URL.Query().Get("cursor"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var nc any
	if next != "" {
		nc = next
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nc})
}

// ---------- statistik & SSE ----------

func (a *API) overview(w http.ResponseWriter, r *http.Request) {
	o, err := a.Svc.Overview(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, o)
}

func (a *API) timeseries(w http.ResponseWriter, r *http.Request) {
	from, to := qTime(r, "from"), qTime(r, "to")
	if from == nil || to == nil {
		a.writeProblem(w, r, core.Validation(core.FieldError{Field: "from/to", Message: "wajib, format RFC 3339"}))
		return
	}
	camp, perr := qUUID(r, "campaign_id")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	sim, perr := qUUID(r, "sim_id")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	pts, err := a.Svc.Timeseries(r.Context(), *from, *to, r.URL.Query().Get("bucket"), camp, sim)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"points": pts})
}

func (a *API) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		a.writeProblem(w, r, core.NewProblem(http.StatusInternalServerError, "internal", "SSE tidak didukung", ""))
		return
	}
	var lastN int64
	hasLast := false
	resync := false
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		var epoch int64
		if _, err := fmt.Sscanf(id, "%d-%d", &epoch, &lastN); err == nil && epoch == a.Bus.Epoch() {
			hasLast = true
		} else {
			resync = true // proses berbeda / id tak valid: klien harus refetch
		}
	}
	ch, replay, ok2, cancel := a.Bus.Subscribe(lastN, hasLast)
	defer cancel()
	if hasLast && !ok2 {
		resync = true
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n\n")
	if resync {
		fmt.Fprint(w, "event: resync\ndata: {}\n\n")
	}
	write := func(e bus.Event) {
		fmt.Fprintf(w, "id: %d-%d\nevent: %s\ndata: %s\n\n", a.Bus.Epoch(), e.ID, e.Name, e.Data)
	}
	for _, e := range replay {
		write(e)
	}
	fl.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			write(e)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

// ---------- campaign ----------

func (a *API) listCampaigns(w http.ResponseWriter, r *http.Request) {
	cs, next, err := a.Svc.ListCampaigns(r.Context(), r.URL.Query().Get("status"), qInt(r, "limit", 50), r.URL.Query().Get("cursor"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var nc any
	if next != "" {
		nc = next
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": cs, "next_cursor": nc})
}

func (a *API) createCampaign(w http.ResponseWriter, r *http.Request) {
	f, ok := a.decodeMap(w, r)
	if !ok {
		return
	}
	p := principalOf(r.Context())
	c, err := a.Svc.CreateCampaign(r.Context(), p.user.ID, r.Header.Get("Idempotency-Key"), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(p), "campaign.create", "campaign", c.ID.String(), map[string]any{"name": c.Name})
	a.writeJSON(w, http.StatusCreated, c)
}

func (a *API) getCampaign(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "campaignId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	c, err := a.Svc.GetCampaign(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, c)
}

func (a *API) patchCampaign(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "campaignId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	f, ok := a.decodeMap(w, r)
	if !ok {
		return
	}
	c, err := a.Svc.PatchCampaign(r.Context(), id, f, actorOf(principalOf(r.Context())))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, c)
}

func (a *API) deleteCampaign(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "campaignId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	if err := a.Svc.DeleteCampaign(r.Context(), id, actorOf(principalOf(r.Context()))); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) importRecipients(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "campaignId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	var recs []core.RecipientInput
	var rows []int
	dedupe := true
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, 21<<20)
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			a.writeProblem(w, r, core.Validation(core.FieldError{Field: "file", Message: "multipart tidak valid atau lebih dari 20 MiB"}))
			return
		}
		if v := r.FormValue("dedupe"); v != "" {
			dedupe = v != "false"
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			a.writeProblem(w, r, core.Validation(core.FieldError{Field: "file", Message: "berkas CSV wajib diunggah (field 'file')"}))
			return
		}
		defer f.Close()
		recs, rows, err = core.ParseRecipientsCSV(f)
		if err != nil {
			a.fail(w, r, err)
			return
		}
	} else {
		var in struct {
			Dedupe     *bool                 `json:"dedupe"`
			Recipients []core.RecipientInput `json:"recipients"`
		}
		if !a.decode(w, r, &in) {
			return
		}
		recs = in.Recipients
		if in.Dedupe != nil {
			dedupe = *in.Dedupe
		}
	}
	res, err := a.Svc.ImportRecipients(r.Context(), id, recs, rows, dedupe)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(principalOf(r.Context())), "campaign.import", "campaign", id.String(), map[string]any{"accepted": res.Accepted, "invalid": len(res.Invalid), "duplicates": res.Duplicates, "suppressed": res.Suppressed})
	a.writeJSON(w, http.StatusOK, res)
}

func (a *API) clearRecipients(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "campaignId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	n, err := a.Svc.ClearRecipients(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"removed": n})
}

func (a *API) campaignAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, perr := pathUUID(r, "campaignId")
		if perr != nil {
			a.writeProblem(w, r, perr)
			return
		}
		actor := actorOf(principalOf(r.Context()))
		var c core.Campaign
		var err error
		switch action {
		case "start":
			c, err = a.Svc.StartCampaign(r.Context(), id, actor)
		case "pause":
			c, err = a.Svc.PauseCampaign(r.Context(), id, actor)
		case "resume":
			c, err = a.Svc.ResumeCampaign(r.Context(), id, actor)
		case "cancel":
			c, err = a.Svc.CancelCampaign(r.Context(), id, actor)
		}
		if err != nil {
			a.fail(w, r, err)
			return
		}
		a.writeJSON(w, http.StatusOK, c)
	}
}

func (a *API) previewCampaign(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "campaignId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	var in struct {
		Sample int `json:"sample"`
	}
	if !a.decode(w, r, &in) {
		return
	}
	p, err := a.Svc.PreviewCampaign(r.Context(), id, in.Sample)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, p)
}
