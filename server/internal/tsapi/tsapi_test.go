package tsapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jipaw/smsc-app/server/internal/tsapi/tsapitest"
)

func TestCreateAndDeleteDeviceKeyWithOAuth(t *testing.T) {
	f := tsapitest.New("cid", "csecret")
	defer f.Close()
	c := &Client{BaseURL: f.URL, Tailnet: "-", ClientID: "cid", ClientSecret: "csecret"}
	ctx := context.Background()
	k1, err := c.CreateDeviceKey(ctx, []string{"tag:smsc-device"}, 15*time.Minute, "smsc enrollment x")
	if err != nil || k1.ID != "k1" || !strings.HasPrefix(k1.Key, "tskey-auth-") {
		t.Fatalf("create: %+v %v", k1, err)
	}
	if _, err := c.CreateDeviceKey(ctx, []string{"tag:smsc-device"}, time.Minute, "y"); err != nil {
		t.Fatal(err)
	}
	tokens, created, _ := f.Snapshot()
	if tokens != 1 {
		t.Fatalf("token OAuth harus di-cache: %d permintaan token", tokens)
	}
	c0 := created[0]
	if c0.Reusable || c0.Ephemeral || !c0.Preauthorized || len(c0.Tags) != 1 || c0.Tags[0] != "tag:smsc-device" || c0.ExpirySeconds != 900 || c0.Description != "smsc enrollment x" {
		t.Fatalf("isi permintaan key: %+v", c0)
	}
	if err := c.DeleteKey(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteKey(ctx, "k1"); err != nil {
		t.Fatalf("404 saat hapus ulang dianggap sukses: %v", err)
	}
	f.SetFailCreates(1)
	if _, err := c.CreateDeviceKey(ctx, []string{"tag:x"}, time.Minute, ""); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("error API harus diteruskan: %v", err)
	}
	bad := &Client{BaseURL: f.URL, Tailnet: "-", ClientID: "cid", ClientSecret: "salah"}
	if _, err := bad.CreateDeviceKey(ctx, []string{"tag:x"}, time.Minute, ""); err == nil || strings.Contains(err.Error(), "salah") {
		t.Fatalf("kredensial salah: %v (secret tidak boleh muncul di error)", err)
	}
}

func TestAPIKeyAuth(t *testing.T) {
	f := tsapitest.New("cid", "csecret")
	defer f.Close()
	// API key ditolak fake (hanya token OAuth yang dikenal) → error 401 diteruskan, tanpa memanggil endpoint token
	c := &Client{BaseURL: f.URL, Tailnet: "-", APIKey: "tskey-api-x"}
	if _, err := c.CreateDeviceKey(context.Background(), []string{"tag:x"}, time.Minute, ""); err == nil {
		t.Fatal("harus 401")
	}
	if tok, _, _ := f.Snapshot(); tok != 0 {
		t.Fatal("API key tidak boleh meminta token OAuth")
	}
}
