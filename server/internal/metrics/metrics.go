// Package metrics: metrik Prometheus (registry per instans, bukan global, agar tes dapat membuat banyak server).
// Nama dan label diatur supaya kardinalitas tetap kecil: tanpa id, nomor, atau isi pesan.
package metrics

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

type Metrics struct {
	Reg *prometheus.Registry

	HTTPRequests *prometheus.CounterVec
	HTTPDuration *prometheus.HistogramVec

	MessagesCreated *prometheus.CounterVec
	Transitions     *prometheus.CounterVec
	LeaseExpired    prometheus.Counter
	EventIngest     *prometheus.CounterVec

	WSConnections prometheus.Gauge
	WSConnects    prometheus.Counter
	WSMessages    *prometheus.CounterVec

	DispatchAssignments prometheus.Counter
	DispatchWait        prometheus.Histogram

	CampaignMaterialized prometheus.Counter

	WebhookDeliveries *prometheus.CounterVec
	EmailResults      *prometheus.CounterVec
	WebhookDuration   prometheus.Histogram

	RetentionRuns prometheus.Counter
	RetentionRows *prometheus.CounterVec

	SCIMReconcileRuns    *prometheus.CounterVec
	SCIMReconcileChanged prometheus.Counter

	ScrapeErrors prometheus.Counter
}

func New(pool *pgxpool.Pool) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{Reg: reg}
	f := func(c prometheus.Collector) { reg.MustRegister(c) }

	m.HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "smsc_http_requests_total", Help: "Permintaan HTTP menurut method, pola rute (bukan URL), dan status."}, []string{"method", "route", "status"})
	m.HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "smsc_http_request_duration_seconds", Help: "Durasi permintaan HTTP.", Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}}, []string{"method", "route"})
	m.MessagesCreated = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "smsc_messages_created_total", Help: "Pesan yang dibuat menurut sumber."}, []string{"source"})
	m.Transitions = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "smsc_message_transitions_total", Help: "Transisi status pesan (dari -> ke)."}, []string{"from", "to"})
	m.LeaseExpired = prometheus.NewCounter(prometheus.CounterOpts{Name: "smsc_lease_expired_total", Help: "Lease pesan yang kedaluwarsa karena HP tidak mengonfirmasi (ack_timeout)."})
	m.EventIngest = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "smsc_event_ingest_total", Help: "Event status SMS dari HP yang diterima menurut state."}, []string{"state"})
	m.WSConnections = prometheus.NewGauge(prometheus.GaugeOpts{Name: "smsc_ws_connections", Help: "Koneksi WebSocket device aktif."})
	m.WSConnects = prometheus.NewCounter(prometheus.CounterOpts{Name: "smsc_ws_connects_total", Help: "Total sesi WebSocket device yang berhasil dibuka (lonjakan = flapping)."})
	m.WSMessages = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "smsc_ws_messages_total", Help: "Pesan WebSocket menurut tipe dan arah (in = dari HP)."}, []string{"type", "dir"})
	m.DispatchAssignments = prometheus.NewCounter(prometheus.CounterOpts{Name: "smsc_dispatch_assignments_total", Help: "Pesan yang di-lease ke SIM."})
	m.DispatchWait = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "smsc_dispatch_wait_seconds", Help: "Waktu tunggu pesan di antrean sampai di-lease.", Buckets: []float64{.1, .5, 1, 5, 15, 60, 300, 900, 3600, 21600, 86400}})
	m.CampaignMaterialized = prometheus.NewCounter(prometheus.CounterOpts{Name: "smsc_campaign_materialized_total", Help: "Penerima campaign yang dijadikan pesan."})
	m.EmailResults = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "smsc_email_total", Help: "Percobaan pengiriman email menurut hasil (sent|retry|failed)."}, []string{"result"})
	m.WebhookDeliveries = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "smsc_webhook_deliveries_total", Help: "Percobaan pengiriman webhook menurut hasil (delivered|retry|failed)."}, []string{"result"})
	m.WebhookDuration = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "smsc_webhook_delivery_duration_seconds", Help: "Durasi satu percobaan pengiriman webhook.", Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10}})
	m.RetentionRuns = prometheus.NewCounter(prometheus.CounterOpts{Name: "smsc_retention_runs_total", Help: "Eksekusi job retensi."})
	m.RetentionRows = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "smsc_retention_rows_total", Help: "Baris yang dibersihkan/dianonimkan retensi menurut jenis."}, []string{"kind"})
	m.SCIMReconcileRuns = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "smsc_scim_reconcile_runs_total", Help: "Run rekonsiliasi grup SCIM pada instance ini menurut hasil (ok|error)."}, []string{"result"})
	m.SCIMReconcileChanged = prometheus.NewCounter(prometheus.CounterOpts{Name: "smsc_scim_reconcile_changed_total", Help: "Pengguna yang perannya dikoreksi oleh rekonsiliasi SCIM (lonjakan = drift atau perubahan pemetaan)."})
	for _, r := range []string{"ok", "error"} {
		m.SCIMReconcileRuns.WithLabelValues(r) // seri ada sejak awal (0)
	}
	m.ScrapeErrors = prometheus.NewCounter(prometheus.CounterOpts{Name: "smsc_scrape_errors_total", Help: "Kegagalan membaca metrik berbasis database saat scrape."})
	for _, c := range []prometheus.Collector{m.HTTPRequests, m.HTTPDuration, m.MessagesCreated, m.Transitions, m.LeaseExpired, m.EventIngest, m.WSConnections, m.WSConnects, m.WSMessages,
		m.DispatchAssignments, m.DispatchWait, m.CampaignMaterialized, m.WebhookDeliveries, m.EmailResults, m.WebhookDuration, m.RetentionRuns, m.RetentionRows, m.SCIMReconcileRuns, m.SCIMReconcileChanged, m.ScrapeErrors} {
		f(c)
	}
	f(collectors.NewGoCollector())
	f(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	if pool != nil {
		f(&dbCollector{pool: pool, m: m})
	}
	return m
}

// dbCollector: gauge yang dihitung saat scrape dari PostgreSQL (di-cache 5 dtk agar scrape berulang tidak membebani DB).
type dbCollector struct {
	pool *pgxpool.Pool
	m    *Metrics
	mu   sync.Mutex
	at   time.Time
	snap []prometheus.Metric
}

var (
	dDevices      = prometheus.NewDesc("smsc_devices", "Jumlah device menurut status.", []string{"status"}, nil)
	dSims         = prometheus.NewDesc("smsc_sims", "Jumlah SIM menurut status.", []string{"status"}, nil)
	dQueue        = prometheus.NewDesc("smsc_queue_depth", "Pesan berstatus queued menurut prioritas.", []string{"priority"}, nil)
	dInflight     = prometheus.NewDesc("smsc_messages_inflight", "Pesan yang sedang diproses HP menurut status (assigned|sending).", []string{"status"}, nil)
	dOldest       = prometheus.NewDesc("smsc_queue_oldest_age_seconds", "Umur pesan antre tertua yang sudah boleh dikirim (0 bila kosong).", nil, nil)
	dLag          = prometheus.NewDesc("smsc_sim_next_eligible_lag_seconds", "Selisih terbesar antara sekarang dan next_eligible_at lane SIM online saat ada antrean (lane siap tetapi belum melepas pesan).", nil, nil)
	dCampaigns    = prometheus.NewDesc("smsc_campaigns", "Jumlah campaign menurut status.", []string{"status"}, nil)
	dWHPending    = prometheus.NewDesc("smsc_webhook_pending_deliveries", "Pengiriman webhook yang menunggu (belum berhasil/gagal final).", nil, nil)
	dEmailPending = prometheus.NewDesc("smsc_email_pending", "Email di outbox yang menunggu dikirim.", nil, nil)
	dEmailOldest  = prometheus.NewDesc("smsc_email_oldest_pending_age_seconds", "Umur email tertunda tertua di outbox.", nil, nil)
	dWHOldest     = prometheus.NewDesc("smsc_webhook_oldest_pending_age_seconds", "Umur pengiriman webhook tertunda tertua.", nil, nil)
	dBattery      = prometheus.NewDesc("smsc_device_battery_percent", "Level baterai terakhir device online/degraded.", []string{"device", "name"}, nil)
	dCharging     = prometheus.NewDesc("smsc_device_charging", "1 bila device sedang mengisi daya.", []string{"device", "name"}, nil)
	dScimLast     = prometheus.NewDesc("smsc_scim_reconcile_last_run_timestamp_seconds", "Waktu run rekonsiliasi SCIM terakhir (dari database; seri tidak ada sebelum run pertama).", nil, nil)
	dScimOK       = prometheus.NewDesc("smsc_scim_reconcile_last_run_ok", "1 bila run rekonsiliasi SCIM terakhir berhasil, 0 bila gagal (dari database).", nil, nil)
	dPool         = prometheus.NewDesc("smsc_db_pool_connections", "Koneksi pool database menurut state.", []string{"state"}, nil)
)

func (c *dbCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{dDevices, dSims, dQueue, dInflight, dOldest, dLag, dCampaigns, dWHPending, dWHOldest, dEmailPending, dEmailOldest, dScimLast, dScimOK, dBattery, dCharging, dPool} {
		ch <- d
	}
}

func (c *dbCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) > 5*time.Second || c.snap == nil {
		c.snap = c.query()
		c.at = time.Now()
	}
	for _, m := range c.snap {
		ch <- m
	}
	st := c.pool.Stat() // statistik pool selalu segar
	for state, v := range map[string]float64{"acquired": float64(st.AcquiredConns()), "idle": float64(st.IdleConns()), "total": float64(st.TotalConns()), "max": float64(st.MaxConns())} {
		ch <- prometheus.MustNewConstMetric(dPool, prometheus.GaugeValue, v, state)
	}
}

func (c *dbCollector) query() []prometheus.Metric {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out []prometheus.Metric
	add := func(d *prometheus.Desc, v float64, lv ...string) {
		out = append(out, prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, lv...))
	}
	group := func(sql string, d *prometheus.Desc, zero ...string) {
		seen := map[string]bool{}
		rows, err := c.pool.Query(ctx, sql)
		if err != nil {
			c.m.ScrapeErrors.Inc()
			return
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var n int64
			if rows.Scan(&k, &n) == nil {
				add(d, float64(n), k)
				seen[k] = true
			}
		}
		for _, z := range zero { // seri selalu ada (nilai 0) agar alert tidak bergantung pada absen-data
			if !seen[z] {
				add(d, 0, z)
			}
		}
	}
	group(`SELECT status::text, count(*) FROM devices GROUP BY 1`, dDevices, "online", "degraded", "offline", "pending", "revoked")
	group(`SELECT status::text, count(*) FROM sims GROUP BY 1`, dSims, "active", "no_service", "absent", "disabled")
	group(`SELECT priority::text, count(*) FROM messages WHERE status='queued' GROUP BY 1`, dQueue, "0", "1", "2", "3", "4", "5", "6", "7", "8", "9")
	group(`SELECT status::text, count(*) FROM messages WHERE status IN ('assigned','sending') GROUP BY 1`, dInflight, "assigned", "sending")
	group(`SELECT status::text, count(*) FROM campaigns GROUP BY 1`, dCampaigns, "draft", "scheduled", "running", "paused", "completed", "cancelled")
	if rows, err := c.pool.Query(ctx, `SELECT id::text, name, battery_level, charging FROM devices WHERE status IN ('online','degraded') AND battery_level IS NOT NULL`); err != nil {
		c.m.ScrapeErrors.Inc()
	} else {
		for rows.Next() {
			var id, name string
			var lvl int
			var ch *bool
			if rows.Scan(&id, &name, &lvl, &ch) == nil {
				add(dBattery, float64(lvl), id, name)
				v := 0.0
				if ch != nil && *ch {
					v = 1
				}
				add(dCharging, v, id, name)
			}
		}
		rows.Close()
	}
	var oldest, lag float64
	if err := c.pool.QueryRow(ctx, `SELECT COALESCE(extract(epoch FROM now() - min(not_before)), 0) FROM messages WHERE status='queued' AND not_before <= now()`).Scan(&oldest); err != nil {
		c.m.ScrapeErrors.Inc()
	}
	add(dOldest, oldest)
	if err := c.pool.QueryRow(ctx, `SELECT COALESCE(max(GREATEST(extract(epoch FROM now() - s.next_eligible_at), 0)), 0)
		FROM sims s JOIN devices d ON d.id=s.device_id
		WHERE s.enabled AND s.status='active' AND d.enabled AND d.status IN ('online','degraded') AND s.next_eligible_at IS NOT NULL
		  AND EXISTS (SELECT 1 FROM messages WHERE status='queued' AND not_before <= now() LIMIT 1)`).Scan(&lag); err != nil {
		c.m.ScrapeErrors.Inc()
	}
	add(dLag, lag)
	var pend int64
	var pendAge float64
	if err := c.pool.QueryRow(ctx, `SELECT count(*), COALESCE(extract(epoch FROM now() - min(created_at)), 0) FROM webhook_deliveries WHERE delivered_at IS NULL AND failed_at IS NULL`).Scan(&pend, &pendAge); err != nil {
		c.m.ScrapeErrors.Inc()
	}
	add(dWHPending, float64(pend))
	add(dWHOldest, pendAge)
	var ePend int64
	var eAge float64
	if err := c.pool.QueryRow(ctx, `SELECT count(*), COALESCE(extract(epoch FROM now() - min(created_at)), 0) FROM email_outbox WHERE status='pending'`).Scan(&ePend, &eAge); err != nil {
		c.m.ScrapeErrors.Inc()
	}
	add(dEmailPending, float64(ePend))
	add(dEmailOldest, eAge)
	var scimAt float64
	var scimErr string
	if err := c.pool.QueryRow(ctx, `SELECT extract(epoch FROM (value->>'at')::timestamptz), COALESCE(value->>'error','') FROM settings WHERE key='scim_reconcile_last'`).Scan(&scimAt, &scimErr); err == nil {
		add(dScimLast, scimAt)
		ok := 1.0
		if scimErr != "" {
			ok = 0
		}
		add(dScimOK, ok)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		c.m.ScrapeErrors.Inc()
	}
	return out
}
