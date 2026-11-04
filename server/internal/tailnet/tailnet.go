// Package tailnet menjalankan smscd sebagai node tailnet sendiri (tsnet) — menggantikan `tailscale serve` — dan
// menempelkan identitas peer (WhoIs) ke setiap permintaan, sehingga server dapat memverifikasi node device.
package tailnet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"

	"github.com/jipaw/smsc-app/server/internal/config"
	"github.com/jipaw/smsc-app/server/internal/core"
)

type Node struct {
	srv *tsnet.Server
	lc  *local.Client
	log *slog.Logger
	ln  net.Listener
	hs  *http.Server
}

// Start menyalakan node dan menunggu tersambung ke tailnet.
func Start(ctx context.Context, cfg config.TailnetConfig, log *slog.Logger) (*Node, error) {
	srv := &tsnet.Server{
		Dir: cfg.StateDir, Hostname: cfg.Hostname, AuthKey: cfg.AuthKey, ControlURL: cfg.ControlURL,
		Logf:     func(string, ...any) {},
		UserLogf: func(f string, a ...any) { log.Info("tailnet: " + fmt.Sprintf(f, a...)) },
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	st, err := srv.Up(cctx)
	if err != nil {
		srv.Close()
		return nil, fmt.Errorf("tailnet: %w", err)
	}
	lc, err := srv.LocalClient()
	if err != nil {
		srv.Close()
		return nil, err
	}
	name := ""
	if st.Self != nil {
		name = st.Self.DNSName
	}
	log.Info("tailnet tersambung", "node", name, "ips", st.TailscaleIPs)
	return &Node{srv: srv, lc: lc, log: log}, nil
}

// Serve melayani handler di tailnet: tls → :443 dengan sertifikat Tailscale (HTTPS harus aktif di tailnet), http → :80.
func (n *Node) Serve(handler http.Handler, mode string) error {
	var ln net.Listener
	var err error
	if mode == "http" {
		ln, err = n.srv.Listen("tcp", ":80")
	} else {
		ln, err = n.srv.ListenTLS("tcp", ":443")
	}
	if err != nil {
		return err
	}
	n.ln = ln
	n.hs = &http.Server{Handler: n.Middleware(handler), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	n.log.Info("melayani di tailnet", "mode", mode, "addr", ln.Addr().String())
	err = n.hs.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Identify: WhoIs untuk alamat peer tailnet ("ip:port" atau "ip") menurut netmap node ini.
func (n *Node) Identify(ctx context.Context, addr string) (*core.TailnetIdentity, error) {
	who, err := n.lc.WhoIs(ctx, addr)
	if err != nil {
		return nil, err
	}
	if who.Node == nil {
		return nil, errors.New("WhoIs tanpa node")
	}
	id := &core.TailnetIdentity{NodeID: string(who.Node.StableID), NodeName: who.Node.ComputedName, Tags: who.Node.Tags, Addr: addr}
	if id.NodeName == "" {
		id.NodeName = who.Node.Name
	}
	if who.UserProfile != nil && len(who.Node.Tags) == 0 {
		id.UserLogin = who.UserProfile.LoginName
	}
	return id, nil
}

// Middleware: WhoIs pada alamat peer → core.TailnetIdentity di context. Gagal WhoIs = 403 (koneksi tailnet tanpa identitas).
func (n *Node) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		id, err := n.Identify(ctx, r.RemoteAddr)
		cancel()
		if err != nil {
			n.log.Warn("tailnet: WhoIs gagal", "addr", r.RemoteAddr, "err", err)
			http.Error(w, "identitas tailnet tidak diketahui", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(core.WithTailnetIdentity(r.Context(), id)))
	})
}

// Shutdown menghentikan listener HTTP lalu node.
func (n *Node) Shutdown(ctx context.Context) {
	if n.hs != nil {
		_ = n.hs.Shutdown(ctx)
	}
	_ = n.srv.Close()
}
