package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jipaw/smsc-app/server/internal/proto"
)

type Sim struct {
	ID             uuid.UUID  `json:"id"`
	DeviceID       uuid.UUID  `json:"device_id"`
	SlotIndex      int        `json:"slot_index"`
	SubscriptionID *int       `json:"subscription_id"`
	CarrierName    *string    `json:"carrier_name"`
	DisplayName    *string    `json:"display_name"`
	PhoneNumber    *string    `json:"phone_number"`
	IsESIM         bool       `json:"is_esim"`
	SignalLevel    *int       `json:"signal_level"`
	Registered     *bool      `json:"registered"`
	Roaming        *bool      `json:"roaming"`
	Label          *string    `json:"label"`
	Enabled        bool       `json:"enabled"`
	Status         string     `json:"status"`
	PolicyID       *uuid.UUID `json:"policy_id"`
	NextEligibleAt *time.Time `json:"next_eligible_at"`
	Today          SimToday   `json:"today"`
	LastSeenAt     *time.Time `json:"last_seen_at"`
}
type SimToday struct {
	Sent      int `json:"sent"`
	Delivered int `json:"delivered"`
	Failed    int `json:"failed"`
}

type Device struct {
	ID             uuid.UUID       `json:"id"`
	Name           string          `json:"name"`
	Status         string          `json:"status"`
	StatusReason   *string         `json:"status_reason"`
	Enabled        bool            `json:"enabled"`
	Manufacturer   *string         `json:"manufacturer"`
	Model          *string         `json:"model"`
	AndroidVersion *string         `json:"android_version"`
	SDKInt         *int            `json:"sdk_int"`
	AppVersion     *string         `json:"app_version"`
	BatteryLevel   *int            `json:"battery_level"`
	Charging       *bool           `json:"charging"`
	NetworkType    *string         `json:"network_type"`
	TailscaleIP    *string         `json:"tailscale_ip"`
	Capabilities   json.RawMessage `json:"capabilities"`
	PolicyID       *uuid.UUID      `json:"policy_id"`
	Tags           []string        `json:"tags"`
	LastSeenAt     *time.Time      `json:"last_seen_at"`
	ConnectedSince *time.Time      `json:"connected_since"`
	CreatedAt      time.Time       `json:"created_at"`
	Sims           []Sim           `json:"sims"`
}

const devCols = `id,name,status,status_reason,enabled,manufacturer,model,android_version,sdk_int,app_version,battery_level,charging,
	network_type,host(tailscale_ip),capabilities,policy_id,tags,last_seen_at,connected_since,created_at`

func scanDevice(r pgx.Row) (Device, error) {
	var d Device
	err := r.Scan(&d.ID, &d.Name, &d.Status, &d.StatusReason, &d.Enabled, &d.Manufacturer, &d.Model, &d.AndroidVersion, &d.SDKInt, &d.AppVersion,
		&d.BatteryLevel, &d.Charging, &d.NetworkType, &d.TailscaleIP, &d.Capabilities, &d.PolicyID, &d.Tags, &d.LastSeenAt, &d.ConnectedSince, &d.CreatedAt)
	if d.Tags == nil {
		d.Tags = []string{}
	}
	d.Sims = []Sim{}
	return d, err
}

const simCols = `s.id,s.device_id,s.slot_index,s.subscription_id,s.carrier_name,s.display_name,s.phone_number,s.is_esim,s.signal_level,s.registered,
	s.roaming,s.label,s.enabled,s.status,s.policy_id,s.next_eligible_at,s.last_seen_at,
	(SELECT count(*) FROM messages m WHERE m.sim_id=s.id AND m.sent_at >= date_trunc('day', now())),
	(SELECT count(*) FROM messages m WHERE m.sim_id=s.id AND m.delivered_at >= date_trunc('day', now())),
	(SELECT count(*) FROM messages m WHERE m.sim_id=s.id AND m.status='failed' AND m.finished_at >= date_trunc('day', now()))`

func scanSim(r pgx.Row) (Sim, error) {
	var x Sim
	err := r.Scan(&x.ID, &x.DeviceID, &x.SlotIndex, &x.SubscriptionID, &x.CarrierName, &x.DisplayName, &x.PhoneNumber, &x.IsESIM, &x.SignalLevel,
		&x.Registered, &x.Roaming, &x.Label, &x.Enabled, &x.Status, &x.PolicyID, &x.NextEligibleAt, &x.LastSeenAt, &x.Today.Sent, &x.Today.Delivered, &x.Today.Failed)
	return x, err
}

func (s *Service) ListSims(ctx context.Context, deviceID *uuid.UUID, status string) ([]Sim, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+simCols+` FROM sims s WHERE ($1::uuid IS NULL OR s.device_id=$1) AND ($2='' OR s.status::text=$2) ORDER BY s.device_id, s.slot_index`, deviceID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Sim{}
	for rows.Next() {
		x, err := scanSim(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Service) GetSim(ctx context.Context, id uuid.UUID) (Sim, error) {
	x, err := scanSim(s.Pool.QueryRow(ctx, `SELECT `+simCols+` FROM sims s WHERE s.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return x, NotFound("SIM")
	}
	return x, err
}

type SimPatch struct {
	Label     *string    `json:"label"`
	Enabled   *bool      `json:"enabled"`
	PolicyID  *uuid.UUID `json:"policy_id"`
	PolicySet bool       `json:"-"`
}

func (s *Service) PatchSim(ctx context.Context, id uuid.UUID, p SimPatch) (Sim, error) {
	ct, err := s.Pool.Exec(ctx, `UPDATE sims SET label=COALESCE($2,label), enabled=COALESCE($3,enabled),
		policy_id = CASE WHEN $4 THEN $5 ELSE policy_id END WHERE id=$1`, id, p.Label, p.Enabled, p.PolicySet, p.PolicyID)
	if err != nil {
		return Sim{}, err
	}
	if ct.RowsAffected() == 0 {
		return Sim{}, NotFound("SIM")
	}
	sim, err := s.GetSim(ctx, id)
	if err == nil {
		s.Bus.Publish("sim.updated", map[string]any{"id": sim.ID, "status": sim.Status, "signal_level": sim.SignalLevel, "next_eligible_at": sim.NextEligibleAt, "enabled": sim.Enabled})
		s.Wake()
	}
	return sim, err
}

func (s *Service) ListDevices(ctx context.Context, status string) ([]Device, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+devCols+` FROM devices WHERE ($1='' OR status::text=$1) ORDER BY created_at`, status)
	if err != nil {
		return nil, err
	}
	devs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Device, error) { return scanDevice(r) })
	if err != nil {
		return nil, err
	}
	sims, err := s.ListSims(ctx, nil, "")
	if err != nil {
		return nil, err
	}
	for i := range devs {
		for _, x := range sims {
			if x.DeviceID == devs[i].ID {
				devs[i].Sims = append(devs[i].Sims, x)
			}
		}
	}
	return devs, nil
}

func (s *Service) GetDevice(ctx context.Context, id uuid.UUID) (Device, error) {
	d, err := scanDevice(s.Pool.QueryRow(ctx, `SELECT `+devCols+` FROM devices WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, NotFound("device")
	}
	if err != nil {
		return d, err
	}
	d.Sims, err = s.ListSims(ctx, &id, "")
	return d, err
}

type DevicePatch struct {
	Name      *string    `json:"name"`
	Enabled   *bool      `json:"enabled"`
	PolicyID  *uuid.UUID `json:"policy_id"`
	PolicySet bool       `json:"-"`
	Tags      []string   `json:"tags"`
}

func (s *Service) PatchDevice(ctx context.Context, id uuid.UUID, p DevicePatch) (Device, error) {
	ct, err := s.Pool.Exec(ctx, `UPDATE devices SET name=COALESCE($2,name), enabled=COALESCE($3,enabled),
		policy_id = CASE WHEN $4 THEN $5 ELSE policy_id END, tags = COALESCE($6,tags) WHERE id=$1`, id, p.Name, p.Enabled, p.PolicySet, p.PolicyID, p.Tags)
	if err != nil {
		return Device{}, err
	}
	if ct.RowsAffected() == 0 {
		return Device{}, NotFound("device")
	}
	s.publishDevice(ctx, id)
	s.Wake()
	return s.GetDevice(ctx, id)
}

func (s *Service) publishDevice(ctx context.Context, id uuid.UUID) {
	if d, err := s.GetDevice(ctx, id); err == nil {
		s.Bus.Publish("device.updated", map[string]any{"id": d.ID, "status": d.Status, "status_reason": d.StatusReason, "battery_level": d.BatteryLevel,
			"network_type": d.NetworkType, "enabled": d.Enabled, "last_seen_at": d.LastSeenAt})
	}
}

// RevokeDevice: kredensial dihapus, WS ditutup 4401; assigned -> queued, sending -> unknown (aturan device-lost).
func (s *Service) RevokeDevice(ctx context.Context, id uuid.UUID) error {
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `UPDATE devices SET status='revoked', revoked_at=now(), connected_since=NULL WHERE id=$1 AND status<>'revoked'`, id)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return NotFound("device")
		}
		if _, err = tx.Exec(ctx, `DELETE FROM device_credentials WHERE device_id=$1`, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id, status FROM messages WHERE device_id=$1 AND status IN ('assigned','sending') FOR UPDATE`, id)
		if err != nil {
			return err
		}
		type m struct {
			id     uuid.UUID
			status string
		}
		var ms []m
		for rows.Next() {
			var x m
			if err := rows.Scan(&x.id, &x.status); err != nil {
				return err
			}
			ms = append(ms, x)
		}
		rows.Close()
		for _, x := range ms {
			to, code := StatusQueued, "device_revoked"
			if x.status == string(StatusSending) {
				to = StatusUnknown
			}
			if _, err := s.transition(ctx, tx, x.id, TransitionOpts{To: to, EventType: "requeued", Actor: "system", ErrorCode: &code, SkipLane: true}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if s.link != nil {
		s.link.Close(id, 4401, "revoked")
	}
	s.publishDevice(ctx, id)
	return nil
}

// ---------- enrollment ----------

type Enrollment struct {
	ID        uuid.UUID `json:"id"`
	Token     string    `json:"token"`
	EnrollURL string    `json:"enroll_url"`
	WebURL    string    `json:"web_url"`
	ShortCode string    `json:"short_code"`
	ExpiresAt time.Time `json:"expires_at"`
}

const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func randCode() string {
	b := []byte(randToken(16))
	out := make([]byte, 0, 8)
	for _, c := range b {
		out = append(out, codeAlphabet[int(c)%len(codeAlphabet)])
		if len(out) == 8 {
			break
		}
	}
	return string(out)
}

func normCode(c string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(c))
}

func (s *Service) CreateEnrollment(ctx context.Context, by uuid.UUID, name string, policyID *uuid.UUID, ttl time.Duration) (Enrollment, error) {
	if ttl <= 0 {
		ttl = s.Cfg.EnrollTTL
	}
	e := Enrollment{ID: newID(), Token: randToken(32), ExpiresAt: time.Now().Add(ttl)}
	code := randCode()
	e.ShortCode = code[:4] + "-" + code[4:]
	e.EnrollURL = "smsc://enroll?s=" + urlEscape(s.Cfg.PublicBaseURL) + "&t=" + e.Token
	e.WebURL = s.Cfg.PublicBaseURL + "/enroll/" + e.Token
	_, err := s.Pool.Exec(ctx, `INSERT INTO device_enrollments(id,token_hash,short_code_hash,device_name,policy_id,created_by,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		e.ID, sha(e.Token), sha(code), nullStr(name), policyID, by, e.ExpiresAt)
	return e, err
}

func (s *Service) RevokeEnrollment(ctx context.Context, id uuid.UUID) error {
	ct, err := s.Pool.Exec(ctx, `UPDATE device_enrollments SET revoked_at=now() WHERE id=$1 AND consumed_at IS NULL AND revoked_at IS NULL`, id)
	if err == nil && ct.RowsAffected() == 0 {
		return NotFound("enrollment")
	}
	return err
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func urlEscape(s string) string {
	return strings.NewReplacer(":", "%3A", "/", "%2F").Replace(s)
}

type EnrollRequest struct {
	Token      string    `json:"token"`
	InstallID  uuid.UUID `json:"install_id"`
	DeviceInfo struct {
		Manufacturer   string `json:"manufacturer"`
		Model          string `json:"model"`
		AndroidVersion string `json:"android_version"`
		SDKInt         int    `json:"sdk_int"`
		AppVersion     string `json:"app_version"`
	} `json:"device_info"`
	Capabilities proto.Capabilities `json:"capabilities"`
	Sims         []struct {
		SlotIndex      int     `json:"slot_index"`
		SubscriptionID int     `json:"subscription_id"`
		CarrierName    *string `json:"carrier_name"`
		DisplayName    *string `json:"display_name"`
		PhoneNumber    *string `json:"phone_number"`
		IsESIM         bool    `json:"is_esim"`
		SignalLevel    *int    `json:"signal_level"`
		Registered     *bool   `json:"registered"`
		Roaming        *bool   `json:"roaming"`
	} `json:"sims"`
}

type EnrollResponse struct {
	DeviceID     uuid.UUID    `json:"device_id"`
	DeviceSecret string       `json:"device_secret"`
	WSURL        string       `json:"ws_url"`
	Config       DeviceConfig `json:"config"`
}

type DeviceConfig struct {
	ProtocolVersion      int `json:"protocol_version"`
	HeartbeatIntervalSec int `json:"heartbeat_interval_sec"`
	WSPingIntervalSec    int `json:"ws_ping_interval_sec"`
	ReconnectBackoff     struct {
		MinMs int `json:"min_ms"`
		MaxMs int `json:"max_ms"`
	} `json:"reconnect_backoff"`
	Guardrails struct {
		MinGapMs     int  `json:"min_gap_ms"`
		MaxPerMinute *int `json:"max_per_minute"`
		OsSmsGuard   struct {
			MaxPerWindow int `json:"max_per_window"`
			WindowSec    int `json:"window_sec"`
		} `json:"os_sms_guard"`
	} `json:"guardrails"`
	ConfigVersion int `json:"config_version"`
}

func (s *Service) deviceConfig(ctx context.Context, q queryer, devPolicy *uuid.UUID, version int) DeviceConfig {
	c := DeviceConfig{ProtocolVersion: 1, HeartbeatIntervalSec: s.Cfg.HeartbeatIntervalSec, WSPingIntervalSec: 20, ConfigVersion: version}
	c.ReconnectBackoff.MinMs, c.ReconnectBackoff.MaxMs = 1000, 60000
	if eff, err := s.Effective(ctx, q, devPolicy, nil, nil); err == nil {
		c.Guardrails.MinGapMs = eff.MinDelayMs
		if eff.RatePerMinute > 0 {
			c.Guardrails.MaxPerMinute = &eff.RatePerMinute
		}
	}
	c.Guardrails.OsSmsGuard.MaxPerWindow, c.Guardrails.OsSmsGuard.WindowSec = 25, 1800
	return c
}

var errBadEnroll = NewProblem(http.StatusUnauthorized, "enrollment.invalid_token", "Token tidak valid", "token atau kode tidak valid, kedaluwarsa, atau sudah dipakai")

// Enroll menukar token (atau short code) menjadi kredensial device. Konsumsi token atomik (spec 07 §2).
func (s *Service) Enroll(ctx context.Context, req EnrollRequest) (EnrollResponse, error) {
	var resp EnrollResponse
	if req.InstallID == uuid.Nil || req.DeviceInfo.SDKInt < 29 {
		return resp, Validation(FieldError{"install_id", "wajib"}, FieldError{"device_info.sdk_int", "minimal 29 (Android 10)"})
	}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		h := sha(req.Token)
		hc := sha(normCode(req.Token))
		var enrID uuid.UUID
		var name *string
		var pol *uuid.UUID
		err := tx.QueryRow(ctx, `UPDATE device_enrollments SET consumed_at=now()
			WHERE (token_hash=$1 OR short_code_hash=$2) AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > now()
			RETURNING id, device_name, policy_id`, h, hc).Scan(&enrID, &name, &pol)
		if errors.Is(err, pgx.ErrNoRows) {
			return errBadEnroll
		}
		if err != nil {
			return err
		}
		devID := newID()
		dname := req.DeviceInfo.Manufacturer + " " + req.DeviceInfo.Model
		if name != nil {
			dname = *name
		}
		caps, _ := json.Marshal(req.Capabilities)
		// re-enroll: install_id sama -> pakai device lama, ganti kredensial
		var existing uuid.UUID
		err = tx.QueryRow(ctx, `SELECT id FROM devices WHERE install_id=$1`, req.InstallID).Scan(&existing)
		reenroll := err == nil
		if reenroll {
			devID = existing
			_, err = tx.Exec(ctx, `UPDATE devices SET status='pending', revoked_at=NULL, manufacturer=$2, model=$3, android_version=$4, sdk_int=$5, app_version=$6, capabilities=$7,
				policy_id=COALESCE($8, policy_id) WHERE id=$1`, devID, req.DeviceInfo.Manufacturer, req.DeviceInfo.Model, req.DeviceInfo.AndroidVersion, req.DeviceInfo.SDKInt, req.DeviceInfo.AppVersion, caps, pol)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO devices(id,name,status,manufacturer,model,android_version,sdk_int,app_version,install_id,capabilities,policy_id)
				VALUES ($1,$2,'pending',$3,$4,$5,$6,$7,$8,$9,$10)`, devID, dname, req.DeviceInfo.Manufacturer, req.DeviceInfo.Model, req.DeviceInfo.AndroidVersion, req.DeviceInfo.SDKInt, req.DeviceInfo.AppVersion, req.InstallID, caps, pol)
		}
		if err != nil {
			return err
		}
		for _, x := range req.Sims {
			if err := upsertSim(ctx, tx, devID, x.SlotIndex, x.SubscriptionID, x.CarrierName, x.DisplayName, x.PhoneNumber, x.IsESIM, x.SignalLevel, x.Registered, x.Roaming); err != nil {
				return err
			}
		}
		secret := "smsd_" + randToken(32)
		if _, err = tx.Exec(ctx, `INSERT INTO device_credentials(device_id, secret_hash) VALUES ($1,$2)
			ON CONFLICT (device_id) DO UPDATE SET secret_hash=EXCLUDED.secret_hash, prev_secret_hash=NULL, prev_valid_until=NULL, rotated_at=now()`, devID, sha(secret)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE device_enrollments SET consumed_by_device_id=$2 WHERE id=$1`, enrID, devID); err != nil {
			return err
		}
		resp = EnrollResponse{DeviceID: devID, DeviceSecret: secret, Config: s.deviceConfig(ctx, tx, pol, 1)}
		resp.WSURL = strings.Replace(strings.Replace(s.Cfg.PublicBaseURL, "https://", "wss://", 1), "http://", "ws://", 1) + "/ws/device"
		if reenroll {
			s.Audit(ctx, "device:"+devID.String(), "device.reenroll", "device", devID.String(), nil)
		}
		return nil
	})
	if err == nil {
		s.publishDevice(ctx, resp.DeviceID)
	}
	return resp, err
}

func upsertSim(ctx context.Context, tx pgx.Tx, devID uuid.UUID, slot, subID int, carrier, display, number *string, esim bool, signal *int, reg, roam *bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO sims(id,device_id,slot_index,subscription_id,carrier_name,display_name,phone_number,is_esim,signal_level,registered,roaming,status,last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'active',now())
		ON CONFLICT (device_id, slot_index) DO UPDATE SET subscription_id=EXCLUDED.subscription_id, carrier_name=EXCLUDED.carrier_name,
		  display_name=EXCLUDED.display_name, phone_number=COALESCE(EXCLUDED.phone_number, sims.phone_number), is_esim=EXCLUDED.is_esim,
		  signal_level=EXCLUDED.signal_level, registered=EXCLUDED.registered, roaming=EXCLUDED.roaming,
		  status = CASE WHEN sims.status='absent' THEN 'active'::sim_status ELSE sims.status END, last_seen_at=now()`,
		newID(), devID, slot, subID, carrier, display, number, esim, signal, reg, roam)
	return err
}

// ApplySimReport menyinkronkan SIM: yang tidak ada di laporan menjadi 'absent' (riwayat tetap valid).
func (s *Service) ApplySimReport(ctx context.Context, devID uuid.UUID, r proto.SimReport) error {
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		slots := []int{}
		for _, x := range r.Sims {
			slots = append(slots, x.SlotIndex)
			if err := upsertSim(ctx, tx, devID, x.SlotIndex, x.SubscriptionID, x.CarrierName, x.DisplayName, x.PhoneNumber, x.IsESIM, x.SignalLevel, x.Registered, x.Roaming); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE sims SET status='absent' WHERE device_id=$1 AND NOT (slot_index = ANY($2)) AND status<>'absent'`, devID, slots)
		return err
	})
	if err == nil {
		s.publishDevice(ctx, devID)
		s.Wake()
	}
	return err
}

// AuthDevice memvalidasi Bearer device_secret (juga secret lama selama masa grace rotasi).
func (s *Service) AuthDevice(ctx context.Context, secret string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.Pool.QueryRow(ctx, `SELECT c.device_id FROM device_credentials c JOIN devices d ON d.id=c.device_id
		WHERE d.status<>'revoked' AND (c.secret_hash=$1 OR (c.prev_secret_hash=$1 AND c.prev_valid_until > now()))`, sha(secret)).Scan(&id)
	if err != nil {
		return uuid.Nil, NewProblem(http.StatusUnauthorized, "auth.unauthenticated", "Tidak terautentikasi", "kredensial device tidak valid")
	}
	return id, nil
}

// ---------- status runtime ----------

func (s *Service) OnConnected(ctx context.Context, id uuid.UUID, h proto.Hello) error {
	caps, _ := json.Marshal(h.Capabilities)
	_, err := s.Pool.Exec(ctx, `UPDATE devices SET status='online', status_reason=NULL, last_seen_at=now(), connected_since=now(), app_version=$2, capabilities=$3 WHERE id=$1 AND status<>'revoked'`,
		id, h.AppVersion, caps)
	if err == nil {
		s.evalAfterRuntime(ctx, id, h.Capabilities.TailscaleVPNActive, nil, nil, nil)
		s.publishDevice(ctx, id)
		s.Wake()
	}
	return err
}

func (s *Service) OnDisconnected(ctx context.Context, id uuid.UUID) {
	_, _ = s.Pool.Exec(ctx, `UPDATE devices SET status='offline', connected_since=NULL WHERE id=$1 AND status IN ('online','degraded','pending')`, id)
	s.publishDevice(ctx, id)
}

// ApplyHeartbeat memperbarui telemetri dan mengevaluasi online/degraded (FR-02 KP2).
func (s *Service) ApplyHeartbeat(ctx context.Context, id uuid.UUID, hb proto.Heartbeat) {
	_, _ = s.Pool.Exec(ctx, `UPDATE devices SET last_seen_at=now(), battery_level=$2, charging=$3, network_type=$4 WHERE id=$1 AND status<>'revoked'`,
		id, hb.Battery.Level, hb.Battery.Charging, hb.Network.Type)
	for _, x := range hb.Sims {
		_, _ = s.Pool.Exec(ctx, `UPDATE sims SET signal_level=$3, registered=$4, roaming=$5, last_seen_at=now() WHERE device_id=$1 AND slot_index=$2`, id, x.SlotIndex, x.SignalLevel, x.Registered, x.Roaming)
	}
	s.evalAfterRuntime(ctx, id, hb.Network.TailscaleVPNActive, &hb.Battery.Level, &hb.Battery.Charging, &hb)
	s.publishDevice(ctx, id)
}

func (s *Service) evalAfterRuntime(ctx context.Context, id uuid.UUID, vpn bool, level *int, charging *bool, hb *proto.Heartbeat) {
	reason := ""
	switch {
	case level != nil && charging != nil && *level < 15 && !*charging:
		reason = "low_battery"
	case hb != nil && len(hb.Sims) > 0 && func() bool {
		for _, x := range hb.Sims {
			if x.Registered == nil || *x.Registered {
				return false
			}
		}
		return true
	}():
		reason = "no_sim_registered"
	case !vpn && s.Cfg.RequireVPN:
		reason = "vpn_inactive"
	case hb != nil && hb.ClockSkewMs != nil && abs64(*hb.ClockSkewMs) > 60000:
		reason = "clock_skew"
	}
	var cur *string
	_ = s.Pool.QueryRow(ctx, `SELECT status_reason FROM devices WHERE id=$1 AND status IN ('online','degraded')`, id).Scan(&cur)
	if cur != nil && *cur == "permission_denied" && reason == "" {
		reason = "permission_denied" // dibersihkan hanya saat hello berikutnya
	}
	if reason == "" {
		_, _ = s.Pool.Exec(ctx, `UPDATE devices SET status='online', status_reason=NULL WHERE id=$1 AND status IN ('online','degraded')`, id)
	} else {
		_, _ = s.Pool.Exec(ctx, `UPDATE devices SET status='degraded', status_reason=$2 WHERE id=$1 AND status IN ('online','degraded')`, id, reason)
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// DeviceConfigFor dipakai endpoint GET /device/config.
func (s *Service) DeviceConfigFor(ctx context.Context, id uuid.UUID) (DeviceConfig, error) {
	var pol *uuid.UUID
	var ver int
	if err := s.Pool.QueryRow(ctx, `SELECT policy_id, config_version FROM devices WHERE id=$1`, id).Scan(&pol, &ver); err != nil {
		return DeviceConfig{}, err
	}
	return s.deviceConfig(ctx, s.Pool, pol, ver), nil
}

// ApplyHelloSims: pembaruan runtime dari hello. Tidak menandai SIM 'absent' dan tidak menimpa metadata
// (display_name, is_esim, nomor) dengan NULL; hanya sim.report yang otoritatif untuk keberadaan SIM.
func (s *Service) ApplyHelloSims(ctx context.Context, devID uuid.UUID, sims []proto.SimRuntime) error {
	for _, x := range sims {
		_, err := s.Pool.Exec(ctx, `INSERT INTO sims(id,device_id,slot_index,subscription_id,carrier_name,signal_level,registered,roaming,status,last_seen_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'active',now())
			ON CONFLICT (device_id, slot_index) DO UPDATE SET
			  subscription_id = COALESCE(EXCLUDED.subscription_id, sims.subscription_id),
			  carrier_name    = COALESCE(EXCLUDED.carrier_name, sims.carrier_name),
			  signal_level = EXCLUDED.signal_level, registered = EXCLUDED.registered, roaming = EXCLUDED.roaming,
			  status = CASE WHEN sims.status='absent' THEN 'active'::sim_status ELSE sims.status END, last_seen_at = now()`,
			newID(), devID, x.SlotIndex, x.SubscriptionID, x.CarrierName, x.SignalLevel, x.Registered, x.Roaming)
		if err != nil {
			return err
		}
	}
	s.publishDevice(ctx, devID)
	return nil
}
