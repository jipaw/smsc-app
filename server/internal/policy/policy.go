// Package policy menyelesaikan dispatch policy berlapis (docs/03 §4, ADR-06).
//
// Guardrail (global -> device -> SIM): nilai paling konservatif menang.
// Preferensi (campaign -> pesan): boleh memperketat, tidak boleh melonggarkan guardrail.
package policy

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Policy: semua field opsional (nil = diwarisi). Nama JSON mengikuti DispatchPolicyInput di OpenAPI.
type Policy struct {
	MinDelayMs        *int    `json:"min_delay_ms,omitempty"`
	MaxDelayMs        *int    `json:"max_delay_ms,omitempty"`
	BurstSize         *int    `json:"burst_size,omitempty"`
	BurstPauseMs      *int    `json:"burst_pause_ms,omitempty"`
	MaxInflightPerSim *int    `json:"max_inflight_per_sim,omitempty"`
	RatePerMinute     *int    `json:"-"`
	RatePerHour       *int    `json:"-"`
	RatePerDay        *int    `json:"-"`
	SendWindow        *Window `json:"send_window,omitempty"`
	MaxAttempts       *int    `json:"max_attempts,omitempty"`
	RetryBackoffMs    *int    `json:"retry_backoff_ms,omitempty"`
	RetryBackoffMaxMs *int    `json:"retry_backoff_max_ms,omitempty"`
	MessageTTLSeconds *int    `json:"message_ttl_seconds,omitempty"`
	TrackDelivery     *bool   `json:"track_delivery,omitempty"`
	AckTimeoutMs      *int    `json:"ack_timeout_ms,omitempty"`
	SentTimeoutMs     *int    `json:"sent_timeout_ms,omitempty"`
	RetryOnUnknown    *bool   `json:"retry_on_unknown,omitempty"`
}

// Window: jendela waktu kirim (OpenAPI SendWindow).
type Window struct {
	Timezone string      `json:"timezone"`
	Windows  []DayWindow `json:"windows"`
}

// DayWindow: days 1=Senin..7=Minggu; start/end "HH:MM". start >= end berarti melewati tengah malam.
type DayWindow struct {
	Days  []int  `json:"days"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// Effective: hasil akhir (tanpa nilai nil).
type Effective struct {
	MinDelayMs        int
	MaxDelayMs        int
	BurstSize         int // 0 = tanpa burst
	BurstPauseMs      int
	MaxInflightPerSim int
	RatePerMinute     int // 0 = tak dibatasi
	RatePerHour       int
	RatePerDay        int
	Windows           []Window // semua harus mengizinkan (irisan)
	MaxAttempts       int
	RetryBackoffMs    int
	RetryBackoffMaxMs int
	MessageTTLSeconds int
	TrackDelivery     bool
	AckTimeoutMs      int
	SentTimeoutMs     int
	RetryOnUnknown    bool
}

var ErrEmptyWindow = errors.New("irisan jendela waktu kosong")

func maxp(cur **int, v *int) {
	if v != nil && (*cur == nil || *v > **cur) {
		x := *v
		*cur = &x
	}
}
func minp(cur **int, v *int) {
	if v != nil && (*cur == nil || *v < **cur) {
		x := *v
		*cur = &x
	}
}
func nearest[T any](levels ...*T) *T { // levels diurutkan dari paling jauh ke paling dekat dengan pesan
	var out *T
	for _, l := range levels {
		if l != nil {
			out = l
		}
	}
	return out
}

func val(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// Resolve: global wajib lengkap (validasi di DB). guard = [device, sim]; pref = [campaign, message].
func Resolve(global Policy, guard []Policy, pref []Policy) (Effective, error) {
	all := append(append([]Policy{global}, guard...), pref...)
	var minD, maxD, burstPause, inflight, burstSize, rpm, rph, rpd *int
	for i, p := range all {
		// delay: ambil maksimum antar SEMUA lapis (preferensi boleh memperbesar, guardrail menahan)
		maxp(&minD, p.MinDelayMs)
		maxp(&maxD, p.MaxDelayMs)
		maxp(&burstPause, p.BurstPauseMs)
		// batas: ambil minimum (paling ketat)
		minp(&inflight, p.MaxInflightPerSim)
		minp(&burstSize, p.BurstSize)
		minp(&rpm, p.RatePerMinute)
		minp(&rph, p.RatePerHour)
		minp(&rpd, p.RatePerDay)
		_ = i
	}
	e := Effective{
		MinDelayMs: val(minD, 0), MaxDelayMs: val(maxD, 0), BurstSize: val(burstSize, 0), BurstPauseMs: val(burstPause, 0),
		MaxInflightPerSim: val(inflight, 1), RatePerMinute: val(rpm, 0), RatePerHour: val(rph, 0), RatePerDay: val(rpd, 0),
	}
	if e.MaxDelayMs < e.MinDelayMs {
		e.MaxDelayMs = e.MinDelayMs
	}
	if e.MaxInflightPerSim < 1 {
		e.MaxInflightPerSim = 1
	}
	for _, p := range all {
		if p.SendWindow != nil && len(p.SendWindow.Windows) > 0 {
			e.Windows = append(e.Windows, *p.SendWindow)
		}
	}
	if len(e.Windows) > 0 && !EverOpen(e.Windows, time.Now()) {
		return e, ErrEmptyWindow
	}
	// field "terdekat menang": pesan > campaign > SIM > device > global (urutan all sudah dari jauh ke dekat)
	pick := func(f func(Policy) *int) *int {
		var o *int
		for _, p := range all {
			if v := f(p); v != nil {
				o = v
			}
		}
		return o
	}
	e.MaxAttempts = val(pick(func(p Policy) *int { return p.MaxAttempts }), 3)
	e.RetryBackoffMs = val(pick(func(p Policy) *int { return p.RetryBackoffMs }), 30000)
	e.RetryBackoffMaxMs = val(pick(func(p Policy) *int { return p.RetryBackoffMaxMs }), 600000)
	e.MessageTTLSeconds = val(pick(func(p Policy) *int { return p.MessageTTLSeconds }), 86400)
	e.AckTimeoutMs = val(pick(func(p Policy) *int { return p.AckTimeoutMs }), 15000)
	e.SentTimeoutMs = val(pick(func(p Policy) *int { return p.SentTimeoutMs }), 120000)
	boolNearest := func(f func(Policy) *bool, def bool) bool {
		var lv []*bool
		for _, p := range all {
			lv = append(lv, f(p))
		}
		if v := nearest(lv...); v != nil {
			return *v
		}
		return def
	}
	e.TrackDelivery = boolNearest(func(p Policy) *bool { return p.TrackDelivery }, true)
	e.RetryOnUnknown = boolNearest(func(p Policy) *bool { return p.RetryOnUnknown }, false)
	return e, nil
}

// ---------- jendela waktu ----------

func parseHM(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("format jam tidak valid %q (HH:MM)", s)
	}
	return h*60 + m, nil
}

// Validate memeriksa zona waktu dan format jam.
func (w Window) Validate() error {
	if _, err := time.LoadLocation(w.Timezone); err != nil {
		return fmt.Errorf("zona waktu tidak dikenal %q", w.Timezone)
	}
	for _, d := range w.Windows {
		if _, err := parseHM(d.Start); err != nil {
			return err
		}
		if _, err := parseHM(d.End); err != nil {
			return err
		}
		if len(d.Days) == 0 {
			return errors.New("days kosong")
		}
		for _, x := range d.Days {
			if x < 1 || x > 7 {
				return fmt.Errorf("hari %d di luar 1..7", x)
			}
		}
	}
	return nil
}

func isoDay(t time.Time) int { // 1=Senin..7=Minggu
	d := int(t.Weekday())
	if d == 0 {
		return 7
	}
	return d
}

func contains(days []int, d int) bool {
	for _, x := range days {
		if x == d {
			return true
		}
	}
	return false
}

// Allows: apakah waktu t berada di dalam salah satu jendela.
func (w Window) Allows(t time.Time) bool {
	loc, err := time.LoadLocation(w.Timezone)
	if err != nil {
		return false
	}
	lt := t.In(loc)
	hm := lt.Hour()*60 + lt.Minute()
	prev := isoDay(lt.AddDate(0, 0, -1))
	today := isoDay(lt)
	for _, d := range w.Windows {
		s, e := mustHM(d.Start), mustHM(d.End)
		if s < e {
			if contains(d.Days, today) && hm >= s && hm < e {
				return true
			}
		} else { // melewati tengah malam
			if (contains(d.Days, today) && hm >= s) || (contains(d.Days, prev) && hm < e) {
				return true
			}
		}
	}
	return false
}

func mustHM(s string) int { v, _ := parseHM(s); return v }

// AllAllow: true bila semua jendela mengizinkan t (irisan).
func AllAllow(ws []Window, t time.Time) bool {
	for _, w := range ws {
		if !w.Allows(t) {
			return false
		}
	}
	return true
}

// NextOpen: waktu terdekat >= t di mana semua jendela terbuka (resolusi 1 menit, maksimum 8 hari).
func NextOpen(ws []Window, t time.Time) (time.Time, bool) {
	if len(ws) == 0 || AllAllow(ws, t) {
		return t, true
	}
	c := t.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 8*24*60; i++ {
		if AllAllow(ws, c) {
			return c, true
		}
		c = c.Add(time.Minute)
	}
	return time.Time{}, false
}

// EverOpen: apakah ada waktu (dalam 8 hari ke depan) yang diizinkan semua jendela.
func EverOpen(ws []Window, t time.Time) bool { _, ok := NextOpen(ws, t); return ok }
