package policy

import (
	"testing"
	"time"
)

func ip(i int) *int   { return &i }
func bp(b bool) *bool { return &b }
func jakarta(h, m int, day time.Weekday) time.Time {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	// 2026-11-02 adalah Senin
	d := time.Date(2026, 11, 2, h, m, 0, 0, loc)
	return d.AddDate(0, 0, (int(day)-int(time.Monday)+7)%7)
}

func global() Policy {
	return Policy{
		MinDelayMs: ip(3000), MaxDelayMs: ip(8000), BurstSize: ip(20), BurstPauseMs: ip(60000), MaxInflightPerSim: ip(1),
		RatePerMinute: ip(10), RatePerHour: ip(300), RatePerDay: ip(2000),
		MaxAttempts: ip(3), RetryBackoffMs: ip(30000), RetryBackoffMaxMs: ip(600000), MessageTTLSeconds: ip(86400),
		TrackDelivery: bp(true), AckTimeoutMs: ip(15000), SentTimeoutMs: ip(120000), RetryOnUnknown: bp(false),
	}
}

func TestDelayGuardrailMenang(t *testing.T) { // T-POL-01
	sim := Policy{MinDelayMs: ip(5000), MaxDelayMs: ip(10000)}
	camp := Policy{MinDelayMs: ip(1000), MaxDelayMs: ip(2000)}
	e, err := Resolve(global(), []Policy{{}, sim}, []Policy{camp, {}})
	if err != nil || e.MinDelayMs != 5000 || e.MaxDelayMs != 10000 {
		t.Fatalf("efektif %d-%d err=%v, mau 5000-10000", e.MinDelayMs, e.MaxDelayMs, err)
	}
}

func TestPreferensiBolehMemperlambat(t *testing.T) {
	e, _ := Resolve(global(), nil, []Policy{{MinDelayMs: ip(20000), MaxDelayMs: ip(30000)}})
	if e.MinDelayMs != 20000 || e.MaxDelayMs != 30000 {
		t.Fatalf("got %d-%d", e.MinDelayMs, e.MaxDelayMs)
	}
}

func TestRateMinimum(t *testing.T) { // T-POL-02
	e, _ := Resolve(global(), []Policy{{RatePerMinute: ip(6)}}, []Policy{{RatePerMinute: ip(20)}})
	if e.RatePerMinute != 6 {
		t.Fatalf("rate/menit=%d, mau 6", e.RatePerMinute)
	}
}

func TestMaxTidakDibawahMin(t *testing.T) {
	e, _ := Resolve(global(), nil, []Policy{{MinDelayMs: ip(50000)}})
	if e.MaxDelayMs < e.MinDelayMs {
		t.Fatalf("max %d < min %d", e.MaxDelayMs, e.MinDelayMs)
	}
}

func TestIrisanJendela(t *testing.T) { // T-POL-03
	w := func(s, e string) *Window {
		return &Window{Timezone: "Asia/Jakarta", Windows: []DayWindow{{Days: []int{1, 2, 3, 4, 5, 6, 7}, Start: s, End: e}}}
	}
	g := global()
	g.SendWindow = w("08:00", "20:00")
	e, err := Resolve(g, []Policy{{SendWindow: w("10:00", "22:00")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !AllAllow(e.Windows, jakarta(15, 0, time.Monday)) || AllAllow(e.Windows, jakarta(9, 0, time.Monday)) || AllAllow(e.Windows, jakarta(21, 0, time.Monday)) {
		t.Fatal("irisan 10-20 salah")
	}
	if _, err = Resolve(g, []Policy{{SendWindow: w("21:00", "22:00")}}, nil); err != ErrEmptyWindow {
		t.Fatalf("mau ErrEmptyWindow, got %v", err)
	}
}

func TestTerdekatMenang(t *testing.T) { // T-POL-04
	camp := Policy{MaxAttempts: ip(5), RetryOnUnknown: bp(true)}
	msg := Policy{MaxAttempts: ip(1)}
	e, _ := Resolve(global(), []Policy{{MaxAttempts: ip(2)}}, []Policy{camp, msg})
	if e.MaxAttempts != 1 || !e.RetryOnUnknown {
		t.Fatalf("attempts=%d retryOnUnknown=%v", e.MaxAttempts, e.RetryOnUnknown)
	}
}

func TestJendelaMelewatiTengahMalamDanNextOpen(t *testing.T) {
	w := Window{Timezone: "Asia/Jakarta", Windows: []DayWindow{{Days: []int{1}, Start: "22:00", End: "02:00"}}} // Senin 22:00 -> Selasa 02:00
	if !w.Allows(jakarta(23, 0, time.Monday)) || !w.Allows(jakarta(1, 0, time.Tuesday)) || w.Allows(jakarta(3, 0, time.Tuesday)) || w.Allows(jakarta(23, 0, time.Tuesday)) {
		t.Fatal("jendela lintas tengah malam salah")
	}
	n, ok := NextOpen([]Window{w}, jakarta(12, 0, time.Monday))
	if !ok || !n.Equal(jakarta(22, 0, time.Monday)) {
		t.Fatalf("NextOpen=%v ok=%v", n, ok)
	}
}

func TestWindowValidate(t *testing.T) {
	if (Window{Timezone: "Mars/Olympus"}).Validate() == nil {
		t.Fatal("zona tak dikenal harus ditolak")
	}
	if (Window{Timezone: "Asia/Jakarta", Windows: []DayWindow{{Days: []int{8}, Start: "08:00", End: "09:00"}}}).Validate() == nil {
		t.Fatal("hari 8 harus ditolak")
	}
}

// T-POL-05: properti - efektif tidak pernah lebih longgar dari guardrail mana pun
func TestPropertiGuardrail(t *testing.T) {
	vals := []int{0, 500, 3000, 8000, 20000}
	for _, a := range vals {
		for _, b := range vals {
			for _, c := range vals {
				g := Policy{MinDelayMs: ip(a), MaxDelayMs: ip(a), RatePerMinute: ip(a + 1), MaxInflightPerSim: ip(1)}
				sim := Policy{MinDelayMs: ip(b), MaxDelayMs: ip(b), RatePerMinute: ip(b + 1)}
				pref := Policy{MinDelayMs: ip(c), MaxDelayMs: ip(c), RatePerMinute: ip(c + 1)}
				e, _ := Resolve(g, []Policy{{}, sim}, []Policy{pref})
				if e.MinDelayMs < a || e.MinDelayMs < b || e.MaxDelayMs < e.MinDelayMs || e.RatePerMinute > a+1 || e.RatePerMinute > b+1 {
					t.Fatalf("melonggarkan guardrail: a=%d b=%d c=%d -> %+v", a, b, c, e)
				}
			}
		}
	}
}
