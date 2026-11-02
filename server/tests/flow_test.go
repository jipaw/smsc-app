package tests

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jipaw/smsc-app/server/internal/proto"
)

const num = "08123456789" // -> +628123456789

func TestEnrollAndOnline(t *testing.T) {
	e := newEnv(t)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin, 0, 1)
	s, ack := e.connect(id, secret, nil)
	_ = s
	if ack.HeartbeatIntervalSec <= 0 || len(ack.Reconcile.Drop) != 0 {
		t.Fatalf("hello.ack aneh: %+v", ack)
	}
	var list struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Sims   []struct {
				SlotIndex int `json:"slot_index"`
			} `json:"sims"`
		} `json:"items"`
	}
	waitFor(t, 3*time.Second, "device online", func() bool {
		admin.do("GET", "/api/v1/devices", nil, &list)
		return len(list.Items) == 1 && list.Items[0].Status == "online"
	})
	if len(list.Items[0].Sims) != 2 {
		t.Fatalf("mau 2 SIM, dapat %d", len(list.Items[0].Sims))
	}
	// token sekali pakai
	var pr struct{ Type string }
	var en struct{ Token string }
	admin.do("POST", "/api/v1/device-enrollments", map[string]any{}, &en)
	body := map[string]any{"token": en.Token, "install_id": uuid.NewString(), "device_info": map[string]any{"sdk_int": 33, "manufacturer": "x", "model": "y", "app_version": "1"}, "sims": []any{}}
	if st := admin.do("POST", "/api/v1/device/enroll", body, nil); st != 201 {
		t.Fatalf("enroll pertama %d", st)
	}
	body["install_id"] = uuid.NewString()
	if st := admin.do("POST", "/api/v1/device/enroll", body, &pr); st != 401 || pr.Type != "enrollment.invalid_token" {
		t.Fatalf("token terpakai harus 401 enrollment.invalid_token, dapat %d %s", st, pr.Type)
	}
}

func TestSendDeliversWithDelay(t *testing.T) { // FR-13 KP1, T-INT-01
	e := newEnv(t)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	var ids []string
	for i := 0; i < 4; i++ {
		ids = append(ids, admin.send(num, "halo")[0].ID)
	}
	for _, id := range ids {
		admin.waitStatus(id, "delivered", 15*time.Second)
	}
	if s.SendCount() != 4 {
		t.Fatalf("mau 4 sms.send, dapat %d", s.SendCount())
	}
	for i := 1; i < 4; i++ { // jeda >= 300 ms (min delay) dihitung dari 'sent' pesan sebelumnya
		gap := s.Sends[i].At.Sub(s.Sends[i-1].At)
		if gap < 250*time.Millisecond {
			t.Errorf("jeda antar sms.send #%d = %v, mau >= ~300ms", i, gap)
		}
	}
	m := admin.msg(ids[0])
	types := map[string]bool{}
	for _, ev := range m.Events {
		types[ev.Type] = true
	}
	for _, want := range []string{"created", "queued", "assigned", "sending", "sent", "delivered"} {
		if !types[want] {
			t.Errorf("timeline tidak memuat %q: %+v", want, m.Events)
		}
	}
	// semua event dari device di-ack server (outbox bisa dibersihkan)
	waitFor(t, 2*time.Second, "event.ack", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.Acks) >= 12 })
}

func TestAckTimeoutRequeuesWithSameAttempt(t *testing.T) { // T-INT-03
	e := newEnv(t)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	n := 0
	s.OnSend = func(s *Sim, env proto.Envelope, cmd proto.SmsSend) {
		n++
		if n == 1 {
			return // abaikan: tidak ada command.ack
		}
		Happy(s, env, cmd)
	}
	m := admin.send(num, "x")[0]
	admin.waitStatus(m.ID, "delivered", 15*time.Second)
	if s.SendCount() != 2 {
		t.Fatalf("mau 2 sms.send (kirim ulang), dapat %d", s.SendCount())
	}
	if s.Sends[0].Cmd.Attempt != 1 || s.Sends[1].Cmd.Attempt != 1 {
		t.Fatalf("attempt harus sama (belum pernah dikonfirmasi): %d,%d", s.Sends[0].Cmd.Attempt, s.Sends[1].Cmd.Attempt)
	}
}

func TestSentTimeoutUnknownThenRecovery(t *testing.T) { // T-INT-04/05, NFR-03
	e := newEnv(t)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	var cmd proto.SmsSend
	s.OnSend = func(s *Sim, env proto.Envelope, c proto.SmsSend) {
		cmd = c
		s.Ack(env, true, "")
		s.Event(c, "sending", "") // lalu hening: tidak ada 'sent'
	}
	m := admin.send(num, "x")[0]
	admin.waitStatus(m.ID, "unknown", 12*time.Second)
	time.Sleep(1500 * time.Millisecond)
	if s.SendCount() != 1 {
		t.Fatalf("tidak boleh ada kirim ulang otomatis untuk unknown (got %d sms.send)", s.SendCount())
	}
	// device kembali melaporkan hasil dari outbox -> koreksi otomatis
	s.Event(cmd, "sent", "")
	admin.waitStatus(m.ID, "sent", 5*time.Second)
	s.Event(cmd, "delivered", "")
	admin.waitStatus(m.ID, "delivered", 5*time.Second)
}

func TestRetryableFailureBacksOffThenSucceeds(t *testing.T) { // T-INT-07
	e := newEnv(t)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	n := 0
	s.OnSend = func(s *Sim, env proto.Envelope, cmd proto.SmsSend) {
		n++
		s.Ack(env, true, "")
		s.Event(cmd, "sending", "")
		if n == 1 {
			s.Event(cmd, "failed", "no_service")
			return
		}
		s.Event(cmd, "sent", "")
		s.Event(cmd, "delivered", "")
	}
	m := admin.send(num, "x")[0]
	admin.waitStatus(m.ID, "delivered", 20*time.Second)
	if s.Sends[1].Cmd.Attempt != 2 {
		t.Fatalf("retry harus attempt 2, dapat %d", s.Sends[1].Cmd.Attempt)
	}
	if gap := s.Sends[1].At.Sub(s.Sends[0].At); gap < 900*time.Millisecond {
		t.Fatalf("backoff minimal ~1s, dapat %v", gap)
	}
}

func TestNonRetryableFailsImmediatelyAndExhaustion(t *testing.T) { // T-INT-08
	e := newEnv(t)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = func(s *Sim, env proto.Envelope, cmd proto.SmsSend) {
		s.Ack(env, true, "")
		s.Event(cmd, "sending", "")
		if cmd.To == "+628123456789" {
			s.Event(cmd, "failed", "invalid_destination")
		} else {
			s.Event(cmd, "failed", "no_service") // retryable, habis attempt
		}
	}
	m := admin.send(num, "x")[0]
	f := admin.waitStatus(m.ID, "failed", 10*time.Second)
	if f.ErrorCode == nil || *f.ErrorCode != "invalid_destination" || s.SendCount() != 1 {
		t.Fatalf("harus gagal langsung tanpa retry: %+v sends=%d", f, s.SendCount())
	}
	m2 := admin.send("08129998877", "y")[0]
	f2 := admin.waitStatus(m2.ID, "failed", 30*time.Second)
	if f2.Attempt != 3 {
		t.Fatalf("harus habis di attempt 3, dapat %d", f2.Attempt)
	}
}

func TestDuplicateAndStaleEventsIgnored(t *testing.T) { // T-INT-09, V2/V3
	e := newEnv(t)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	var cmd proto.SmsSend
	s.OnSend = func(s *Sim, env proto.Envelope, c proto.SmsSend) { cmd = c; s.Ack(env, true, "") }
	m := admin.send(num, "x")[0]
	admin.waitStatus(m.ID, "sending", 5*time.Second)
	evid := s.Event(cmd, "sent", "")
	admin.waitStatus(m.ID, "sent", 5*time.Second)
	// event yang sama dikirim ulang (outbox)
	dup := proto.SmsEvent{EventID: evid, MessageID: cmd.MessageID, Attempt: cmd.Attempt, State: "sent", PartsTotal: 1, At: time.Now().UTC().Format(time.RFC3339Nano)}
	s.EventRaw(dup)
	// event attempt lama -> stale
	s.EventRaw(proto.SmsEvent{EventID: uuid.NewString(), MessageID: cmd.MessageID, Attempt: cmd.Attempt + 5, State: "failed", ErrorCode: ptrS("no_service"), PartsTotal: 1, At: time.Now().UTC().Format(time.RFC3339Nano)})
	time.Sleep(600 * time.Millisecond)
	got := admin.msg(m.ID)
	if got.Status != "sent" {
		t.Fatalf("status harus tetap sent, dapat %s", got.Status)
	}
	var sentEvents, stale int
	for _, ev := range got.Events {
		if ev.Type == "sent" {
			sentEvents++
		}
		if ev.Type == "stale_event" {
			stale++
		}
	}
	if sentEvents != 1 || stale != 1 {
		t.Fatalf("sent=%d (mau 1) stale=%d (mau 1): %+v", sentEvents, stale, got.Events)
	}
}

func TestIdempotencyKey(t *testing.T) { // T-INT-16
	e := newEnv(t)
	admin := e.loginAs("admin")
	req := map[string]any{"recipients": []map[string]any{{"to": num}}, "body": "sama"}
	var a, b struct {
		Messages []msgView `json:"messages"`
	}
	h := map[string]string{"Idempotency-Key": "inv-1"}
	if st, _ := admin.doH("POST", "/api/v1/messages", req, &a, h); st != 202 {
		t.Fatal(st)
	}
	if st, _ := admin.doH("POST", "/api/v1/messages", req, &b, h); st != 202 || b.Messages[0].ID != a.Messages[0].ID {
		t.Fatalf("pengulangan harus mengembalikan pesan yang sama: %d %+v %+v", st, a, b)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM messages`).Scan(&n)
	if n != 1 {
		t.Fatalf("mau 1 baris pesan, dapat %d", n)
	}
	req["body"] = "beda"
	var pr struct{ Type string }
	if st, _ := admin.doH("POST", "/api/v1/messages", req, &pr, h); st != 409 || pr.Type != "idempotency.conflict" {
		t.Fatalf("isi berbeda harus 409 idempotency.conflict, dapat %d %s", st, pr.Type)
	}
}

func TestValidationSuppressionAndClientRef(t *testing.T) { // FR-06 KP2, FR-15
	e := newEnv(t)
	admin := e.loginAs("admin")
	if st := admin.do("POST", "/api/v1/suppressions", map[string]any{"numbers": []string{"08111222333"}, "reason": "opt-out"}, nil); st != 201 {
		t.Fatal(st)
	}
	var out struct {
		Messages []msgView `json:"messages"`
		Rejected []struct {
			Index  int    `json:"index"`
			Reason string `json:"reason"`
		} `json:"rejected"`
	}
	st := admin.do("POST", "/api/v1/messages", map[string]any{"body": "Halo {{name}}", "recipients": []map[string]any{
		{"to": "08123456789", "vars": map[string]string{"name": "A"}, "client_ref": "r1"},
		{"to": "bukan-nomor"},
		{"to": "08111222333", "vars": map[string]string{"name": "B"}},
		{"to": "08129998877"}, // variabel hilang
	}}, &out)
	if st != 202 || len(out.Messages) != 1 {
		t.Fatalf("status %d messages=%d", st, len(out.Messages))
	}
	reasons := map[int]string{}
	for _, r := range out.Rejected {
		reasons[r.Index] = r.Reason
	}
	if reasons[1] != "invalid_number" || reasons[2] != "suppressed" || reasons[3] != "missing_variable" {
		t.Fatalf("alasan penolakan salah: %+v", reasons)
	}
	// semua ditolak -> 422
	if st := admin.do("POST", "/api/v1/messages", map[string]any{"body": "x", "recipients": []map[string]any{{"to": "abc"}}}, nil); st != 422 {
		t.Fatalf("semua ditolak harus 422, dapat %d", st)
	}
	// body/template wajib salah satu
	if st := admin.do("POST", "/api/v1/messages", map[string]any{"recipients": []map[string]any{{"to": num}}}, nil); st != 422 {
		t.Fatalf("tanpa body harus 422, dapat %d", st)
	}
}

func TestReconcileOnHello(t *testing.T) { // T-INT-10
	e := newEnv(t)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	var cmd proto.SmsSend
	s.OnSend = func(s *Sim, env proto.Envelope, c proto.SmsSend) {
		cmd = c
		s.Ack(env, true, "")
		s.Event(c, "sending", "")
	}
	m := admin.send(num, "x")[0]
	admin.waitStatus(m.ID, "sending", 5*time.Second)
	_ = cmd
	s.cancel()
	_ = s.conn.CloseNow()
	waitFor(t, 3*time.Second, "device offline", func() bool {
		var d struct{ Status string }
		admin.do("GET", "/api/v1/devices/"+id.String(), nil, &d)
		return d.Status == "offline"
	})
	// kembali tanpa job itu (app di-reinstall) + mengklaim job yang tidak dikenal server
	ghost := uuid.NewString()
	_, ack := e.connect(id, secret, []proto.Inflight{{MessageID: ghost, Attempt: 1, State: "sending"}})
	if len(ack.Reconcile.Drop) != 1 || ack.Reconcile.Drop[0] != ghost {
		t.Fatalf("drop harus berisi ghost: %+v", ack.Reconcile)
	}
	got := admin.waitStatus(m.ID, "unknown", 5*time.Second)
	if got.ErrorCode == nil || *got.ErrorCode != "device_lost" {
		t.Fatalf("error_code %v", got.ErrorCode)
	}
}

func TestRevokeClosesSocketAndRejectsReconnect(t *testing.T) { // FR-04
	e := newEnv(t)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	if st := admin.do("DELETE", "/api/v1/devices/"+id.String(), nil, nil); st != 204 {
		t.Fatal(st)
	}
	select {
	case ce := <-s.Closed:
		if ce.Code != 4401 {
			t.Fatalf("kode close %d, mau 4401", ce.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WS tidak ditutup setelah revoke")
	}
	st, _ := admin.doH("GET", "/ws/device", nil, nil, map[string]string{"Authorization": "Bearer " + secret, "Sec-WebSocket-Protocol": "smsc.v1"})
	if st != 401 {
		t.Fatalf("reconnect setelah revoke harus 401, dapat %d", st)
	}
}

func TestSecondSessionSupersedesFirst(t *testing.T) { // S4
	e := newEnv(t)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s1, _ := e.connect(id, secret, nil)
	e.connect(id, secret, nil)
	select {
	case ce := <-s1.Closed:
		if ce.Code != 4409 {
			t.Fatalf("mau 4409, dapat %d", ce.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sesi lama tidak ditutup")
	}
}

func TestSubprotocolRequired(t *testing.T) {
	e := newEnv(t)
	admin := e.loginAs("admin")
	_, secret := e.enroll(admin)
	st, _ := admin.doH("GET", "/ws/device", nil, nil, map[string]string{"Authorization": "Bearer " + secret})
	if st != 400 {
		t.Fatalf("tanpa subprotocol harus 400, dapat %d", st)
	}
}

func TestKillSwitchPausesDispatch(t *testing.T) { // spec 09 §8
	e := newEnv(t)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	if st := admin.do("PUT", "/api/v1/settings/dispatch-pause", map[string]any{"paused": true}, nil); st != 200 {
		t.Fatal(st)
	}
	m := admin.send(num, "x")[0]
	time.Sleep(1500 * time.Millisecond)
	if s.SendCount() != 0 || admin.msg(m.ID).Status != "queued" {
		t.Fatal("saat pause tidak boleh ada pengiriman")
	}
	admin.do("PUT", "/api/v1/settings/dispatch-pause", map[string]any{"paused": false}, nil)
	admin.waitStatus(m.ID, "delivered", 10*time.Second)
}

func TestSendWindowBlocksAndRateLimit(t *testing.T) { // T-INT-11/12
	e := newEnv(t)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	// batas 2 per menit: dari 4 pesan hanya 2 yang boleh keluar
	e.sql(`UPDATE dispatch_policies SET rate_per_minute=2, min_delay_ms=0, max_delay_ms=0 WHERE is_default`)
	var ids []string
	for i := 0; i < 4; i++ {
		ids = append(ids, admin.send(num, "x")[0].ID)
	}
	time.Sleep(4 * time.Second)
	if s.SendCount() != 2 {
		t.Fatalf("rate 2/menit: mau 2 pengiriman, dapat %d", s.SendCount())
	}
	// jendela tertutup: tidak ada pengiriman
	e.sql(`UPDATE dispatch_policies SET rate_per_minute=NULL, send_window=jsonb_build_object('timezone','UTC','windows',
		jsonb_build_array(jsonb_build_object('days',jsonb_build_array(1,2,3,4,5,6,7),'start',to_char((now() at time zone 'UTC') + interval '2 hours','HH24:MI'),
		'end',to_char((now() at time zone 'UTC') + interval '3 hours','HH24:MI')))) WHERE is_default`)
	s.mu.Lock()
	before := len(s.Sends)
	s.mu.Unlock()
	time.Sleep(2 * time.Second)
	if s.SendCount() != before {
		t.Fatal("di luar jendela waktu tidak boleh ada pengiriman")
	}
	_ = ids
}

func TestCancelQueuedAndConflictWhenSending(t *testing.T) { // T-INT-24
	e := newEnv(t)
	admin := e.loginAs("admin")
	// tanpa device: pesan tetap queued -> bisa dibatalkan
	q := admin.send(num, "x")[0]
	var c msgView
	if st := admin.do("POST", "/api/v1/messages/"+q.ID+"/cancel", nil, &c); st != 200 || c.Status != "cancelled" {
		t.Fatalf("cancel queued: %d %s", st, c.Status)
	}
	var pr struct{ Type string }
	if st := admin.do("POST", "/api/v1/messages/"+q.ID+"/cancel", nil, &pr); st != 409 || pr.Type != "message.invalid_transition" {
		t.Fatalf("cancel kedua harus 409, dapat %d %s", st, pr.Type)
	}
	// sudah sending -> 409
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = func(s *Sim, env proto.Envelope, cmd proto.SmsSend) { s.Ack(env, true, ""); s.Event(cmd, "sending", "") }
	m := admin.send(num, "y")[0]
	admin.waitStatus(m.ID, "sending", 5*time.Second)
	if st := admin.do("POST", "/api/v1/messages/"+m.ID+"/cancel", nil, &pr); st != 409 {
		t.Fatalf("cancel saat sending harus 409, dapat %d", st)
	}
}

func TestRetryUnknownRequiresConfirmation(t *testing.T) { // FR-12 KP3
	e := newEnv(t)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	n := 0
	s.OnSend = func(s *Sim, env proto.Envelope, cmd proto.SmsSend) {
		n++
		if n == 1 {
			s.Ack(env, true, "")
			s.Event(cmd, "sending", "")
			return
		}
		Happy(s, env, cmd)
	}
	m := admin.send(num, "x")[0]
	admin.waitStatus(m.ID, "unknown", 12*time.Second)
	if st := admin.do("POST", "/api/v1/messages/"+m.ID+"/retry", map[string]any{}, nil); st != 422 {
		t.Fatalf("retry unknown tanpa konfirmasi harus 422, dapat %d", st)
	}
	var r msgView
	if st := admin.do("POST", "/api/v1/messages/"+m.ID+"/retry", map[string]any{"confirm_duplicate_risk": true}, &r); st != 200 {
		t.Fatalf("retry dikonfirmasi: %d", st)
	}
	admin.waitStatus(m.ID, "delivered", 10*time.Second)
}

func TestMultiSimLoadsBothLanes(t *testing.T) { // T-INT-01: tiap SIM serial, SIM berbeda paralel
	e := newEnv(t)
	admin := e.loginAs("admin")
	e.sql(`UPDATE dispatch_policies SET min_delay_ms=1000, max_delay_ms=1000 WHERE is_default`)
	id, secret := e.enroll(admin, 0, 1)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	for i := 0; i < 4; i++ {
		admin.send(num, "x")
	}
	waitFor(t, 15*time.Second, "4 pesan terkirim", func() bool { return s.SendCount() == 4 })
	bySlot := map[int][]time.Time{}
	s.mu.Lock()
	for _, r := range s.Sends {
		bySlot[r.Cmd.Sim.SlotIndex] = append(bySlot[r.Cmd.Sim.SlotIndex], r.At)
	}
	s.mu.Unlock()
	if len(bySlot) != 2 {
		t.Fatalf("kedua SIM harus dipakai: %v", bySlot)
	}
	for slot, ts := range bySlot {
		for i := 1; i < len(ts); i++ {
			if ts[i].Sub(ts[i-1]) < 900*time.Millisecond {
				t.Errorf("slot %d: jeda %v < 1s", slot, ts[i].Sub(ts[i-1]))
			}
		}
	}
}

func TestRBACAndCSRFAndAPIKey(t *testing.T) { // FR-17, FR-18
	e := newEnv(t)
	admin := e.loginAs("admin")
	viewer := e.loginAs("viewer")
	operator := e.loginAs("operator")
	anon := &client{e: e, http: &httpClientNoJar}

	if st := anon.do("GET", "/api/v1/devices", nil, nil); st != 401 {
		t.Fatalf("anonim harus 401, dapat %d", st)
	}
	if st := viewer.do("POST", "/api/v1/messages", map[string]any{"recipients": []map[string]any{{"to": num}}, "body": "x"}, nil); st != 403 {
		t.Fatalf("viewer kirim harus 403, dapat %d", st)
	}
	if st := operator.do("POST", "/api/v1/device-enrollments", map[string]any{}, nil); st != 403 {
		t.Fatalf("operator enroll harus 403, dapat %d", st)
	}
	if st := operator.do("POST", "/api/v1/messages", map[string]any{"recipients": []map[string]any{{"to": num}}, "body": "x"}, nil); st != 202 {
		t.Fatalf("operator kirim harus 202, dapat %d", st)
	}
	// CSRF
	saved := admin.csrf
	admin.csrf = "salah"
	var pr struct{ Type string }
	if st := admin.do("POST", "/api/v1/messages", map[string]any{"recipients": []map[string]any{{"to": num}}, "body": "x"}, &pr); st != 403 || pr.Type != "auth.csrf" {
		t.Fatalf("CSRF salah harus 403 auth.csrf, dapat %d %s", st, pr.Type)
	}
	admin.csrf = saved
	// viewer melihat nomor termasking
	var list struct {
		Items []struct{ To string } `json:"items"`
	}
	viewer.do("GET", "/api/v1/messages", nil, &list)
	if len(list.Items) == 0 || list.Items[0].To == "+628123456789" {
		t.Fatalf("viewer harus melihat nomor termasking: %+v", list.Items)
	}
	// API key: scope messages:send saja
	var k struct{ Secret string }
	if st := admin.do("POST", "/api/v1/api-keys", map[string]any{"name": "billing", "scopes": []string{"messages:send"}}, &k); st != 201 || k.Secret == "" {
		t.Fatalf("buat key: %d", st)
	}
	keyc := &client{e: e, http: &httpClientNoJar, key: k.Secret}
	if st := keyc.do("POST", "/api/v1/messages", map[string]any{"recipients": []map[string]any{{"to": num, "client_ref": "inv-9"}}, "body": "x"}, nil); st != 202 {
		t.Fatalf("API key kirim: %d", st)
	}
	if st := keyc.do("POST", "/api/v1/messages", map[string]any{"recipients": []map[string]any{{"to": num, "client_ref": "inv-9"}}, "body": "x"}, nil); st != 422 {
		t.Fatalf("client_ref ganda (semua ditolak) harus 422, dapat %d", st)
	}
	if st := keyc.do("GET", "/api/v1/messages", nil, nil); st != 403 {
		t.Fatalf("scope read tidak ada -> 403, dapat %d", st)
	}
	if st := keyc.do("GET", "/api/v1/devices", nil, nil); st != 403 {
		t.Fatalf("API key tidak boleh akses devices: %d", st)
	}
	// login salah / kunci akun
	for i := 0; i < 5; i++ {
		anon.do("POST", "/api/v1/auth/login", map[string]string{"email": "tak@ada.com", "password": "x"}, nil)
	}
	// device_secret tidak berlaku di API admin (T: batas skema auth)
	_, secret := e.enroll(admin)
	dc := &client{e: e, http: &httpClientNoJar, key: secret}
	if st := dc.do("GET", "/api/v1/devices", nil, nil); st != 401 {
		t.Fatalf("device_secret di rute admin harus 401, dapat %d", st)
	}
}

func TestSSEPublishesMessageUpdates(t *testing.T) { // FR-19
	e := newEnv(t)
	admin := e.loginAs("admin")
	req, _ := newReq(e, "GET", "/api/v1/events/stream")
	resp, err := admin.http.Do(req.WithContext(ctxTimeout(t, 8*time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type %s", resp.Header.Get("Content-Type"))
	}
	m := admin.send(num, "x")[0]
	got := readSSEUntil(t, resp.Body, "message.updated", m.ID)
	if got == "" {
		t.Fatal("event message.updated tidak diterima")
	}
}
