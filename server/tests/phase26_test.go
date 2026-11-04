package tests

import (
	"context"
	"testing"
	"time"
)

// jumlah gagal parsial di Overview (hari ini), deret waktu, dan counters campaign; selalu bagian dari failed.
func TestPartialCountsInOverviewTimeseriesAndCampaign(t *testing.T) {
	gen := "generic_failure"
	e, admin, dev, s := newTakeoverEnv(t)
	camp := admin.newCampaign(map[string]any{"name": "parsial", "body": "halo"})
	s.OnStatus = stateWith("failed", 3, 1, 0, &gen)
	partial := craftSending(t, e, admin, dev, "4 seconds")
	e.sql(`UPDATE messages SET parts=3, campaign_id=$2 WHERE id=$1`, partial, camp.ID)
	e.svc.ReapOnceForTest(context.Background())
	waitStatusIs(t, e, partial, "failed")
	// kegagalan biasa (bukan parsial) pada campaign yang sama dan di luar campaign
	plain := craftSending(t, e, admin, dev, "1 second")
	e.sql(`UPDATE messages SET status='failed', error_code='generic_failure', finished_at=now(), campaign_id=$2 WHERE id=$1`, plain, camp.ID)
	plain2 := craftSending(t, e, admin, dev, "1 second")
	e.sql(`UPDATE messages SET status='failed', error_code='generic_failure', finished_at=now() WHERE id=$1`, plain2)

	var ov struct {
		Today struct{ Failed, Partial int } `json:"today"`
	}
	if st := admin.do("GET", "/api/v1/stats/overview", nil, &ov); st != 200 || ov.Today.Failed != 3 || ov.Today.Partial != 1 {
		t.Fatalf("overview: %d %+v", st, ov)
	}
	var ts struct {
		Points []struct{ Failed, Partial int } `json:"points"`
	}
	from, to := time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if st := admin.do("GET", "/api/v1/stats/timeseries?bucket=hour&from="+from+"&to="+to, nil, &ts); st != 200 {
		t.Fatalf("timeseries: %d", st)
	}
	f, p := 0, 0
	for _, pt := range ts.Points {
		f, p = f+pt.Failed, p+pt.Partial
		if pt.Partial > pt.Failed {
			t.Fatalf("partial harus bagian dari failed: %+v", pt)
		}
	}
	if f != 3 || p != 1 {
		t.Fatalf("deret waktu failed=%d partial=%d", f, p)
	}
	var cs struct {
		Counters struct{ Failed, Partial int } `json:"counters"`
	}
	if st := admin.do("GET", "/api/v1/campaigns/"+camp.ID, nil, &cs); st != 200 || cs.Counters.Failed != 2 || cs.Counters.Partial != 1 {
		t.Fatalf("campaign: %d %+v", st, cs)
	}
	var list struct {
		Items []struct {
			ID       string
			Counters struct{ Failed, Partial int }
		}
	}
	admin.do("GET", "/api/v1/campaigns", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Counters.Partial != 1 {
		t.Fatalf("daftar campaign: %+v", list)
	}
	// retry terkonfirmasi menghapus penanda => angka turun
	if st := admin.do("POST", "/api/v1/messages/"+partial+"/retry", map[string]any{"confirm_duplicate_risk": true}, nil); st != 200 {
		t.Fatalf("retry: %d", st)
	}
	admin.do("GET", "/api/v1/campaigns/"+camp.ID, nil, &cs)
	admin.do("GET", "/api/v1/stats/overview", nil, &ov)
	if cs.Counters.Partial != 0 || cs.Counters.Failed != 1 || ov.Today.Partial != 0 {
		t.Fatalf("setelah retry: %+v %+v", cs, ov)
	}
}
