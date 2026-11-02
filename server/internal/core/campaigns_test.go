package core

import (
	"math"
	"testing"

	"github.com/jipaw/smsc-app/server/internal/policy"
)

func TestEstimateDuration(t *testing.T) {
	def := policy.Effective{MinDelayMs: 3000, MaxDelayMs: 8000, BurstSize: 20, BurstPauseMs: 60000, RatePerMinute: 10, RatePerHour: 300, RatePerDay: 2000}
	near := func(got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 0.5 {
			t.Fatalf("got %.1fs, want %.1fs", got, want)
		}
	}
	// 6 pesan, 2 SIM: 3 pesan/SIM x (5.5+2+3=10.5s); batas harian TIDAK mendominasi (regresi: sebelumnya ~2 menit)
	near(estimateDuration(def, 2, 6), 3*10.5)
	// 1 SIM, 5000 pesan: batas harian 2000 -> (ceil(5000/2000)-1)=2 hari penuh melampaui waktu dasar
	near(estimateDuration(def, 1, 5000), 2*86400)
	// 100 pesan, 1 SIM: batas per jam 300 tidak tercapai; dasar 100*10.5 vs per menit (ceil(100/10)-1)*60=540 -> dasar menang
	near(estimateDuration(def, 1, 100), 1050)
	// tanpa batas: hanya dasar, dan dibagi rata ke SIM
	none := policy.Effective{MinDelayMs: 1000, MaxDelayMs: 1000}
	near(estimateDuration(none, 4, 100), 25*3)
	// tak ada SIM / tak ada pesan
	near(estimateDuration(def, 0, 10), 0)
	near(estimateDuration(def, 2, 0), 0)
}
