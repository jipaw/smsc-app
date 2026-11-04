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

	// gagal tetapi SEMUA bagian terkirim (hitungan disegarkan dari HP mencapai total) pada campaign yang sama
	allSent := craftSending(t, e, admin, dev, "1 second")
	e.sql(`UPDATE messages SET status='failed', error_code='generic_failure', finished_at=now(), parts=3, partial_sent=3, campaign_id=$2 WHERE id=$1`, allSent, camp.ID)

	var ov struct {
		Today struct {
			Failed, Partial int
			AllSent         int `json:"all_sent"`
		} `json:"today"`
	}
	if st := admin.do("GET", "/api/v1/stats/overview", nil, &ov); st != 200 || ov.Today.Failed != 4 || ov.Today.Partial != 2 || ov.Today.AllSent != 1 {
		t.Fatalf("overview: %d %+v", st, ov)
	}
	var ts struct {
		Points []struct {
			Failed, Partial int
			AllSent         int `json:"all_sent"`
		} `json:"points"`
	}
	from, to := time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if st := admin.do("GET", "/api/v1/stats/timeseries?bucket=hour&from="+from+"&to="+to, nil, &ts); st != 200 {
		t.Fatalf("timeseries: %d", st)
	}
	f, p, a := 0, 0, 0
	for _, pt := range ts.Points {
		f, p, a = f+pt.Failed, p+pt.Partial, a+pt.AllSent
		if pt.AllSent > pt.Partial || pt.Partial > pt.Failed {
			t.Fatalf("all_sent ⊆ partial ⊆ failed: %+v", pt)
		}
	}
	if f != 4 || p != 2 || a != 1 {
		t.Fatalf("deret waktu failed=%d partial=%d all_sent=%d", f, p, a)
	}
	var cs struct {
		Counters struct {
			Failed, Partial int
			AllSent         int `json:"all_sent"`
		} `json:"counters"`
	}
	if st := admin.do("GET", "/api/v1/campaigns/"+camp.ID, nil, &cs); st != 200 || cs.Counters.Failed != 3 || cs.Counters.Partial != 2 || cs.Counters.AllSent != 1 {
		t.Fatalf("campaign: %d %+v", st, cs)
	}
	var list struct {
		Items []struct {
			ID       string
			Counters struct {
				Failed, Partial int
				AllSent         int `json:"all_sent"`
			}
		}
	}
	admin.do("GET", "/api/v1/campaigns", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Counters.Partial != 2 || list.Items[0].Counters.AllSent != 1 {
		t.Fatalf("daftar campaign: %+v", list)
	}
	// retry terkonfirmasi menghapus penanda => angka turun
	if st := admin.do("POST", "/api/v1/messages/"+partial+"/retry", map[string]any{"confirm_duplicate_risk": true}, nil); st != 200 {
		t.Fatalf("retry: %d", st)
	}
	admin.do("GET", "/api/v1/campaigns/"+camp.ID, nil, &cs)
	admin.do("GET", "/api/v1/stats/overview", nil, &ov)
	if cs.Counters.Partial != 1 || cs.Counters.AllSent != 1 || cs.Counters.Failed != 2 || ov.Today.Partial != 1 || ov.Today.AllSent != 1 {
		t.Fatalf("setelah retry: %+v %+v", cs, ov)
	}
}
