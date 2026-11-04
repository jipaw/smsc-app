package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/net/netns"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"

	"github.com/jipaw/smsc-app/server/internal/config"
	"github.com/jipaw/smsc-app/server/internal/proto"
	"github.com/jipaw/smsc-app/server/internal/tailnet"
	"github.com/jipaw/smsc-app/server/internal/tsapi/tsapitest"
)

// ---------- lab tailnet in-process: server kontrol uji Tailscale + DERP lokal ----------

type tnLab struct {
	t       *testing.T
	control *testcontrol.Server
	url     string
	base    string // http://smsc lewat tailnet
	smscd   *tailnet.Node
}

func newTnLab(t *testing.T) *tnLab {
	t.Helper()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })
	derpMap := integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1")
	c := &testcontrol.Server{DERPMap: derpMap, DNSConfig: &tailcfg.DNSConfig{Proxied: true}, MagicDNSDomain: "tail-lab.ts.net", Logf: logger.Discard}
	c.HTTPTestServer = httptest.NewUnstartedServer(c)
	c.HTTPTestServer.Start()
	t.Cleanup(c.HTTPTestServer.Close)
	return &tnLab{t: t, control: c, url: c.HTTPTestServer.URL, base: "http://smsc"}
}

// serveSmscd: router smscd di tailnet (node "smsc", mode http) memakai paket tailnet produksi (WhoIs → identitas).
func (l *tnLab) serveSmscd(e *env) {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	n, err := tailnet.Start(ctx, config.TailnetConfig{Hostname: "smsc", StateDir: l.t.TempDir(), ControlURL: l.url}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		l.t.Fatal(err)
	}
	l.smscd = n
	go n.Serve(e.srv.Config.Handler, "http")
	l.t.Cleanup(func() { n.Shutdown(context.Background()) })
}

// node: "HP" di tailnet. tags diberikan lewat control (setara auth key ber-tag).
type tnNode struct {
	srv  *tsnet.Server
	http *http.Client
	id   string
	ip   string
}

func (l *tnLab) node(name string, tags ...string) *tnNode {
	l.t.Helper()
	s := &tsnet.Server{Dir: l.t.TempDir(), ControlURL: l.url, Hostname: name, Store: new(mem.Store), Ephemeral: true, Logf: logger.Discard}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := s.Up(ctx)
	if err != nil {
		l.t.Fatal(err)
	}
	l.t.Cleanup(func() { s.Close() })
	n := &tnNode{srv: s, id: string(st.Self.ID), ip: st.TailscaleIPs[0].String()}
	n.http = &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: s.Dial}}
	if len(tags) > 0 {
		l.tag(n, st.Self.PublicKey, tags)
	}
	return n
}

// tag mengganti tag node di control lalu menunggu sampai node smscd melihatnya (netmap tersebar asinkron).
func (l *tnLab) tag(n *tnNode, pub any, tags []string) {
	l.t.Helper()
	st, _ := n.srv.Up(context.Background())
	node := l.control.Node(st.Self.PublicKey)
	if node == nil {
		l.t.Fatal("node tidak ada di control")
	}
	node = node.Clone()
	node.Tags = tags
	l.control.UpdateNode(node)
	if l.smscd == nil {
		return
	}
	waitFor(l.t, 10*time.Second, "smscd melihat tag baru", func() bool {
		id, err := l.smscd.Identify(context.Background(), n.ip)
		return err == nil && strings.Join(id.Tags, ",") == strings.Join(tags, ",")
	})
}

// get: GET lewat tailnet; menunggu peer "smsc" terlihat di netmap (beberapa ratus ms setelah Up).
func (n *tnNode) do(t *testing.T, method, u string, body any) (int, []byte) {
	t.Helper()
	var last error
	for i := 0; i < 100; i++ {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, u, rd)
		req.Header.Set("Content-Type", "application/json")
		resp, err := n.http.Do(req)
		if err == nil {
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, b
		}
		last = err
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("%s %s lewat tailnet: %v", method, u, last)
	return 0, nil
}

// wsHello: dial /ws/device dengan secret; 101 + hello.ack = 200, selain itu status HTTP penolakan.
func wsHello(t *testing.T, hc *http.Client, base, secret string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, strings.Replace(base, "http", "ws", 1)+"/ws/device", &websocket.DialOptions{
		HTTPClient: hc, HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}}, Subprotocols: []string{"smsc.v1"},
	})
	if err != nil {
		if resp == nil {
			t.Fatalf("dial: %v", err)
		}
		var p struct{ Type string }
		b, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(b, &p)
		return resp.StatusCode, p.Type
	}
	defer conn.CloseNow()
	hello, _ := proto.New(proto.TypeHello, proto.Hello{AppVersion: "tn", Protocol: 1, Sims: []proto.SimRuntime{{SlotIndex: 0, Registered: ptrB(true)}}}, nil)
	b, _ := json.Marshal(hello)
	conn.Write(ctx, websocket.MessageText, b)
	_, raw, err := conn.Read(ctx)
	var env proto.Envelope
	if err != nil || json.Unmarshal(raw, &env) != nil || env.Type != proto.TypeHelloAck {
		t.Fatalf("hello.ack: %v %s", err, raw)
	}
	return 200, ""
}

func enrollBody(token string) map[string]any {
	return map[string]any{"token": token, "install_id": uuid.NewString(),
		"device_info": map[string]any{"manufacturer": "Uji", "model": "Tailnet", "sdk_int": 34, "app_version": "tn"},
		"sims":        []map[string]any{{"slot_index": 0, "registered": true}}}
}

func newEnrollment(t *testing.T, admin *client) string {
	t.Helper()
	var en struct{ Token string }
	if st := admin.do("POST", "/api/v1/device-enrollments", map[string]any{}, &en); st != 201 {
		t.Fatalf("enrollment: %d", st)
	}
	return en.Token
}

func probType(b []byte) string {
	var p struct{ Type string }
	_ = json.Unmarshal(b, &p)
	return p.Type
}

func auditCount(e *env, action string) (n int) {
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action=$1`, action).Scan(&n)
	return
}

// enforce: enroll/WS hanya dari node tailnet ber-tag device; device terikat ke node-nya — secret yang dicuri tidak berguna dari node lain.
func TestTailnetEnforceDeviceIdentity(t *testing.T) {
	lab := newTnLab(t)
	e := newEnvOpts(t, func(c *config.Config) {
		c.Tailnet = config.TailnetConfig{Enabled: true, DevicePolicy: "enforce", DeviceTags: []string{"tag:smsc-device"}}
	})
	lab.serveSmscd(e)
	admin := e.loginAs("admin")
	tok := newEnrollment(t, admin)

	// loopback (bukan tailnet) ditolak, dan token tidak hangus
	var p struct{ Type string }
	if st := (&client{e: e, http: http.DefaultClient}).do("POST", "/api/v1/device/enroll", enrollBody(tok), &p); st != 403 || p.Type != "device.tailnet_required" {
		t.Fatalf("enroll loopback: %d %s", st, p.Type)
	}
	// node tanpa tag ditolak
	untagged := lab.node("hp-tanpa-tag")
	if st, b := untagged.do(t, "POST", lab.base+"/api/v1/device/enroll", enrollBody(tok)); st != 403 || probType(b) != "device.tailnet_tag" {
		t.Fatalf("enroll tanpa tag: %d %s", st, b)
	}
	// node ber-tag diterima; identitas node tersimpan
	hp := lab.node("hp-sah", "tag:smsc-device")
	var out struct {
		DeviceID string `json:"device_id"`
		Secret   string `json:"device_secret"`
	}
	st, b := hp.do(t, "POST", lab.base+"/api/v1/device/enroll", enrollBody(tok))
	if st != 201 || json.Unmarshal(b, &out) != nil {
		t.Fatalf("enroll ber-tag: %d %s", st, b)
	}
	var dev struct {
		Tailnet *struct {
			NodeID   string   `json:"node_id"`
			NodeName string   `json:"node_name"`
			Tags     []string `json:"tags"`
		} `json:"tailnet"`
	}
	admin.do("GET", "/api/v1/devices/"+out.DeviceID, nil, &dev)
	if dev.Tailnet == nil || dev.Tailnet.NodeID != hp.id || len(dev.Tailnet.Tags) != 1 || !strings.HasPrefix(dev.Tailnet.NodeName, "hp-sah") {
		t.Fatalf("identitas tailnet device: %+v (node %s)", dev.Tailnet, hp.id)
	}
	// WebSocket dari node yang sama: diterima
	if st, _ := wsHello(t, hp.http, lab.base, out.Secret); st != 200 {
		t.Fatalf("WS node sah: %d", st)
	}
	// secret yang sama dari loopback / dari node ber-tag LAIN: ditolak 403 dan diaudit
	if st, typ := wsHello(t, http.DefaultClient, e.srv.URL, out.Secret); st != 403 || typ != "device.tailnet_required" {
		t.Fatalf("WS loopback: %d %s", st, typ)
	}
	thief := lab.node("hp-lain", "tag:smsc-device")
	if st, typ := wsHello(t, thief.http, lab.base, out.Secret); st != 403 || typ != "device.tailnet_node_mismatch" {
		t.Fatalf("WS node lain: %d %s", st, typ)
	}
	req, _ := http.NewRequest("GET", lab.base+"/api/v1/device/config", nil)
	req.Header.Set("Authorization", "Bearer "+out.Secret)
	if resp, err := thief.http.Do(req); err != nil || resp.StatusCode != 403 {
		t.Fatalf("HTTP device dari node lain harus 403: %v %v", resp, err)
	}
	if auditCount(e, "device.tailnet_mismatch") < 2 {
		t.Fatal("ketidakcocokan node harus diaudit")
	}
	// tag node device dicabut admin tailnet → node yang sama pun ditolak
	st0, _ := hp.srv.Up(context.Background())
	lab.tag(hp, st0.Self.PublicKey, []string{"tag:lain"})
	if st, typ := wsHello(t, hp.http, lab.base, out.Secret); st != 403 || typ != "device.tailnet_tag" {
		t.Fatalf("WS setelah tag dicabut: %d %s", st, typ)
	}
	// dashboard tetap dapat diakses lewat tailnet; rate limit/penguncian memakai identitas node (bukan IP)
	if st, _ := hp.do(t, "GET", lab.base+"/healthz", nil); st != 200 {
		t.Fatalf("healthz lewat tailnet: %d", st)
	}
}

// record: tidak menolak, tetapi mengikat device ke node pertama (trust on first use) dan mengaudit ketidakcocokan.
func TestTailnetRecordPolicy(t *testing.T) {
	lab := newTnLab(t)
	e := newEnvOpts(t, func(c *config.Config) {
		c.Tailnet = config.TailnetConfig{Enabled: true, DevicePolicy: "record", DeviceTags: []string{"tag:smsc-device"}}
	})
	lab.serveSmscd(e)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin) // lewat loopback: diterima, belum terikat
	var dev struct {
		Tailnet *struct {
			NodeID string `json:"node_id"`
		} `json:"tailnet"`
	}
	admin.do("GET", "/api/v1/devices/"+id.String(), nil, &dev)
	if dev.Tailnet != nil {
		t.Fatal("enroll lewat loopback belum punya identitas tailnet")
	}
	a := lab.node("hp-a") // tanpa tag: record tidak menolak
	if st, _ := wsHello(t, a.http, lab.base, secret); st != 200 {
		t.Fatalf("record: WS dari tailnet harus diterima: %d", st)
	}
	admin.do("GET", "/api/v1/devices/"+id.String(), nil, &dev)
	if dev.Tailnet == nil || dev.Tailnet.NodeID != a.id || auditCount(e, "device.tailnet_bound") != 1 {
		t.Fatalf("record: harus terikat ke node pertama: %+v", dev.Tailnet)
	}
	b := lab.node("hp-b")
	if st, _ := wsHello(t, b.http, lab.base, secret); st != 200 {
		t.Fatalf("record: node lain tetap diterima: %d", st)
	}
	if auditCount(e, "device.tailnet_mismatch") != 1 {
		t.Fatal("record: ketidakcocokan harus diaudit")
	}
	admin.do("GET", "/api/v1/devices/"+id.String(), nil, &dev)
	if dev.Tailnet.NodeID != a.id {
		t.Fatal("record: ikatan tidak boleh berpindah diam-diam")
	}
}

// auth key otomatis: dibuat lewat Tailscale API (OAuth), sekali pakai/pre-authorized/ber-tag, umur = TTL enrollment,
// disisipkan ke QR, dicabut saat enrollment dipakai atau dicabut.
func TestEnrollmentMintsTailnetAuthKey(t *testing.T) {
	fake := tsapitest.New("cid", "rahasia-oauth")
	defer fake.Close()
	e := newEnvOpts(t, func(c *config.Config) {
		c.Tailnet = config.TailnetConfig{DevicePolicy: "off", DeviceTags: []string{"tag:smsc-device"}, APIURL: fake.URL, Tailnet: "-", OAuthClientID: "cid", OAuthClientSecret: "rahasia-oauth"}
	})
	admin := e.loginAs("admin")
	var en struct {
		ID             string  `json:"id"`
		Token          string  `json:"token"`
		EnrollURL      string  `json:"enroll_url"`
		TailnetAuthKey *string `json:"tailnet_auth_key"`
	}
	if st := admin.do("POST", "/api/v1/device-enrollments", map[string]any{"device_name": "kasir", "ttl_seconds": 600}, &en); st != 201 || en.TailnetAuthKey == nil {
		t.Fatalf("enrollment: %d %+v", st, en)
	}
	u, _ := url.Parse(en.EnrollURL)
	if u.Query().Get("k") != *en.TailnetAuthKey || u.Query().Get("t") != en.Token {
		t.Fatalf("enroll_url harus memuat k=: %s", en.EnrollURL)
	}
	_, created, _ := fake.Snapshot()
	c := created[0]
	if c.Reusable || c.Ephemeral || !c.Preauthorized || len(c.Tags) != 1 || c.Tags[0] != "tag:smsc-device" || c.ExpirySeconds != 600 || !strings.Contains(c.Description, "kasir") {
		t.Fatalf("permintaan key: %+v", c)
	}
	// key tidak disimpan di DB (hanya id)
	var stored *string
	_ = e.pool.QueryRow(context.Background(), `SELECT tailnet_key_id FROM device_enrollments WHERE id=$1`, en.ID).Scan(&stored)
	if stored == nil || *stored != c.ID {
		t.Fatalf("id key: %v", stored)
	}
	// dipakai → key dicabut
	var out struct {
		DeviceID string `json:"device_id"`
	}
	if st := (&client{e: e, http: http.DefaultClient}).do("POST", "/api/v1/device/enroll", enrollBody(en.Token), &out); st != 201 {
		t.Fatalf("enroll: %d", st)
	}
	waitFor(t, 5*time.Second, "key dicabut setelah dipakai", func() bool { _, _, d := fake.Snapshot(); return len(d) == 1 && d[0] == c.ID })
	// enrollment kedua: token OAuth di-cache; dicabut → key dicabut
	var en2 struct{ ID string }
	admin.do("POST", "/api/v1/device-enrollments", map[string]any{}, &en2)
	if st := admin.do("DELETE", "/api/v1/device-enrollments/"+en2.ID, nil, nil); st != 204 {
		t.Fatalf("cabut: %d", st)
	}
	tokens, created, deleted := fake.Snapshot()
	if tokens != 1 || len(created) != 2 || len(deleted) != 2 || deleted[1] != created[1].ID {
		t.Fatalf("token=%d created=%d deleted=%v", tokens, len(created), deleted)
	}
	// tailnet_auth_key=false → tanpa key
	var en3 struct {
		EnrollURL      string  `json:"enroll_url"`
		TailnetAuthKey *string `json:"tailnet_auth_key"`
	}
	admin.do("POST", "/api/v1/device-enrollments", map[string]any{"tailnet_auth_key": false}, &en3)
	if en3.TailnetAuthKey != nil || strings.Contains(en3.EnrollURL, "&k=") {
		t.Fatalf("tanpa key: %+v", en3)
	}
	// API gagal → 502, tidak ada enrollment setengah jadi
	var before, after int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM device_enrollments`).Scan(&before)
	fake.SetFailCreates(1)
	var p struct{ Type string }
	if st := admin.do("POST", "/api/v1/device-enrollments", map[string]any{}, &p); st != 502 || p.Type != "tailnet.auth_key_failed" {
		t.Fatalf("API gagal: %d %s", st, p.Type)
	}
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM device_enrollments`).Scan(&after)
	if after != before {
		t.Fatal("enrollment tidak boleh dibuat bila key gagal")
	}
	var aud int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE data::text LIKE '%tskey-auth%'`).Scan(&aud)
	if aud != 0 {
		t.Fatal("secret auth key tidak boleh masuk audit")
	}
}

func TestEnrollmentTailnetKeyRequiresAPI(t *testing.T) {
	e := newEnv(t)
	admin := e.loginAs("admin")
	var p struct{ Type string }
	if st := admin.do("POST", "/api/v1/device-enrollments", map[string]any{"tailnet_auth_key": true}, &p); st != 422 || p.Type != "tailnet.api_not_configured" {
		t.Fatalf("tanpa API: %d %s", st, p.Type)
	}
	var en struct {
		TailnetAuthKey *string `json:"tailnet_auth_key"`
	}
	if st := admin.do("POST", "/api/v1/device-enrollments", map[string]any{}, &en); st != 201 || en.TailnetAuthKey != nil {
		t.Fatalf("default tanpa API: %d", st)
	}
}

