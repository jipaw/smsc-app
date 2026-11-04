// Package proto: envelope dan payload WebSocket v1 (contracts/device-ws-protocol.md, contracts/ws-schema.json).
package proto

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const Version = 1

type Envelope struct {
	V       int             `json:"v"`
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Ref     *string         `json:"ref"`
	TS      string          `json:"ts"`
	Payload json.RawMessage `json:"payload"`
}

const (
	TypeHello         = "hello"
	TypeHelloAck      = "hello.ack"
	TypeHeartbeat     = "heartbeat"
	TypeSmsSend       = "sms.send"
	TypeSmsCancel     = "sms.cancel"
	TypeSmsStatus     = "sms.status"
	TypeSmsState      = "sms.state"
	TypeCommandAck    = "command.ack"
	TypeSmsEvent      = "sms.event"
	TypeEventAck      = "event.ack"
	TypeSimReport     = "sim.report"
	TypeSimRefresh    = "sim.refresh"
	TypeConfigUpdate  = "config.update"
	TypeDeviceCommand = "device.command"
	TypeCommandResult = "device.command_result"
	TypeError         = "error"
)

// New membuat envelope baru dengan id UUIDv7.
func New(typ string, payload any, ref *string) (Envelope, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}
	id, _ := uuid.NewV7()
	return Envelope{V: Version, Type: typ, ID: id.String(), Ref: ref, TS: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), Payload: b}, nil
}

type SimRuntime struct {
	SlotIndex      int     `json:"slot_index"`
	SubscriptionID *int    `json:"subscription_id,omitempty"`
	CarrierName    *string `json:"carrier_name,omitempty"`
	Registered     *bool   `json:"registered,omitempty"`
	SignalLevel    *int    `json:"signal_level,omitempty"`
	Roaming        *bool   `json:"roaming,omitempty"`
}

type Capabilities struct {
	DefaultSmsApp              bool `json:"default_sms_app"`
	OsSmsLimitUnlocked         bool `json:"os_sms_limit_unlocked"`
	BatteryOptimizationIgnored bool `json:"battery_optimization_ignored"`
	NotificationsEnabled       bool `json:"notifications_enabled"`
	RestrictedSettingsAllowed  bool `json:"restricted_settings_allowed"`
	TailscaleVPNActive         bool `json:"tailscale_vpn_active"`
}

type Inflight struct {
	MessageID      string `json:"message_id"`
	Attempt        int    `json:"attempt"`
	State          string `json:"state"`
	PartsTotal     *int   `json:"parts_total,omitempty"`
	PartsSent      *int   `json:"parts_sent,omitempty"`
	PartsDelivered *int   `json:"parts_delivered,omitempty"`
}

type Hello struct {
	AppVersion    string       `json:"app_version"`
	Protocol      int          `json:"protocol"`
	ConfigVersion int          `json:"config_version"`
	Capabilities  Capabilities `json:"capabilities"`
	Sims          []SimRuntime `json:"sims"`
	Inflight      []Inflight   `json:"inflight"`
	PendingEvents int          `json:"pending_events"`
}

type Reconcile struct {
	Drop []string `json:"drop"`
	Keep []string `json:"keep"`
}

type HelloAck struct {
	SessionID            string          `json:"session_id"`
	HeartbeatIntervalSec int             `json:"heartbeat_interval_sec"`
	ConfigVersion        int             `json:"config_version"`
	Config               json.RawMessage `json:"config,omitempty"`
	Reconcile            Reconcile       `json:"reconcile"`
}

type Heartbeat struct {
	Battery struct {
		Level        int      `json:"level"`
		Charging     bool     `json:"charging"`
		TemperatureC *float64 `json:"temperature_c,omitempty"`
	} `json:"battery"`
	Network struct {
		Type               string `json:"type"`
		TailscaleVPNActive bool   `json:"tailscale_vpn_active"`
		RttMs              *int   `json:"rtt_ms,omitempty"`
	} `json:"network"`
	Sims            []SimRuntime `json:"sims"`
	QueueDepthLocal int          `json:"queue_depth_local"`
	ClockSkewMs     *int64       `json:"clock_skew_ms,omitempty"`
	UptimeSec       *int64       `json:"uptime_sec,omitempty"`
}

type SimRef struct {
	SlotIndex         int  `json:"slot_index"`
	SubscriptionIDHnt *int `json:"subscription_id_hint,omitempty"`
}

type SmsSend struct {
	MessageID      string `json:"message_id"`
	Attempt        int    `json:"attempt"`
	Sim            SimRef `json:"sim"`
	To             string `json:"to"`
	Body           string `json:"body"`
	DeliveryReport bool   `json:"delivery_report"`
	NotAfter       string `json:"not_after"`
	MinGapMs       *int   `json:"min_gap_ms,omitempty"`
}

type SmsCancel struct {
	MessageID string `json:"message_id"`
}

// SmsStatus (S→D): tanya keadaan job di device. SmsState (D→S, ref = id SmsStatus): jawabannya.
type SmsStatus struct {
	MessageID string `json:"message_id"`
	Attempt   int    `json:"attempt"`
}

type SmsState struct {
	MessageID      string  `json:"message_id"`
	Attempt        int     `json:"attempt"`
	State          string  `json:"state"` // unknown | accepted | sending | sent | delivered | failed
	PartsTotal     *int    `json:"parts_total,omitempty"`
	PartsSent      *int    `json:"parts_sent,omitempty"`
	PartsDelivered *int    `json:"parts_delivered,omitempty"`
	ErrorCode      *string `json:"error_code,omitempty"`
}

type CommandAck struct {
	Accepted bool    `json:"accepted"`
	Reason   *string `json:"reason"`
}

type SmsEvent struct {
	EventID           string  `json:"event_id"`
	MessageID         string  `json:"message_id"`
	Attempt           int     `json:"attempt"`
	State             string  `json:"state"`
	PartIndex         *int    `json:"part_index,omitempty"`
	PartsTotal        int     `json:"parts_total"`
	ErrorCode         *string `json:"error_code,omitempty"`
	AndroidResultCode *int    `json:"android_result_code,omitempty"`
	At                string  `json:"at"`
}

type EventAck struct {
	EventIDs []string `json:"event_ids"`
}

type SimReport struct {
	Sims []struct {
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

type DeviceCommand struct {
	CommandID string `json:"command_id"`
	Action    string `json:"action"`
	// NewSecret hanya untuk rotate_credential (server → device lewat WSS). Tidak boleh dicatat di log mana pun.
	NewSecret *string `json:"new_secret,omitempty"`
}

type CommandResult struct {
	CommandID string  `json:"command_id"`
	OK        bool    `json:"ok"`
	Detail    *string `json:"detail,omitempty"`
}

type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal"`
}
