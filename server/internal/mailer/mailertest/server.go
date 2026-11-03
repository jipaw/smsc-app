// Package mailertest: server SMTP palsu untuk pengujian (STARTTLS, TLS implisit, AUTH PLAIN, penolakan terprogram).
// Hanya dipakai oleh tes; tidak diimpor oleh kode produksi.
package mailertest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Message struct {
	From, To string
	Data     string // mentah, termasuk header
}

type Server struct {
	Host string
	Port int
	Pool *x509.CertPool // percayai sertifikat server ini

	mu         sync.Mutex
	msgs       []Message
	ln         net.Listener
	tlsCfg     *tls.Config
	implicit   bool
	startTLS   bool
	user, pw   string
	rcptFails  []int // kode balasan RCPT yang dipakai berurutan (mis. 451, 0 = sukses); habis → sukses
	rcptAlways int   // bila ≠ 0, semua RCPT dijawab kode ini
	conns      int
}

type Options struct {
	Implicit   bool   // TLS implisit
	StartTLS   bool   // tawarkan STARTTLS
	User, Pass string // bila diisi, AUTH PLAIN diwajibkan sebelum MAIL
	Bind       string // alamat IP pendengar (default 127.0.0.1)
}

func NewCert() (tls.Certificate, *x509.CertPool) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}, IsCA: true, BasicConstraintsValid: true,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	pool := x509.NewCertPool()
	c, _ := x509.ParseCertificate(der)
	pool.AddCert(c)
	return cert, pool
}

func Start(o Options) *Server {
	bind := o.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	s := &Server{Host: bind, implicit: o.Implicit, startTLS: o.StartTLS, user: o.User, pw: o.Pass}
	ln, err := net.Listen("tcp", bind+":0")
	if err != nil {
		panic(err)
	}
	if o.Implicit || o.StartTLS {
		cert, pool := NewCert()
		s.Pool = pool
		s.tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	if o.Implicit {
		ln = tls.NewListener(ln, s.tlsCfg)
	}
	s.ln = ln
	s.Port = ln.Addr().(*net.TCPAddr).Port
	go s.accept()
	return s
}

func (s *Server) Close() { _ = s.ln.Close() }

// FailRcpt: n balasan RCPT berikutnya memakai kode ini (mis. 451 sementara, 550 permanen).
func (s *Server) FailRcpt(code, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < n; i++ {
		s.rcptFails = append(s.rcptFails, code)
	}
}

func (s *Server) FailRcptAlways(code int) { s.mu.Lock(); s.rcptAlways = code; s.mu.Unlock() }

func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.msgs...)
}

func (s *Server) Connections() int { s.mu.Lock(); defer s.mu.Unlock(); return s.conns }

func (s *Server) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns++
		s.mu.Unlock()
		go s.serve(c)
	}
}

func (s *Server) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	r := bufio.NewReader(c)
	w := func(f string, a ...any) { fmt.Fprintf(c, f+"\r\n", a...) }
	tlsOn := s.implicit
	authed := s.user == ""
	var from, to string
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
			w("250-fake")
			if s.startTLS && !tlsOn {
				w("250-STARTTLS")
			}
			if s.user != "" {
				w("250-AUTH PLAIN")
			}
			w("250 8BITMIME")
		case up == "STARTTLS":
			if !s.startTLS || tlsOn {
				w("503 tidak tersedia")
				continue
			}
			w("220 siap")
			tc := tls.Server(c, s.tlsCfg)
			if err := tc.Handshake(); err != nil {
				return
			}
			c, r, tlsOn = tc, bufio.NewReader(tc), true
			w = func(f string, a ...any) { fmt.Fprintf(tc, f+"\r\n", a...) }
		case strings.HasPrefix(up, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 && parts[1] == s.user && parts[2] == s.pw {
				authed = true
				w("235 ok")
			} else {
				w("535 5.7.8 kredensial salah")
			}
		case strings.HasPrefix(up, "MAIL FROM:"):
			if !authed {
				w("530 5.7.0 autentikasi diperlukan")
				continue
			}
			from = addrOf(line[len("MAIL FROM:"):])
			w("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			s.mu.Lock()
			code := s.rcptAlways
			if code == 0 && len(s.rcptFails) > 0 {
				code, s.rcptFails = s.rcptFails[0], s.rcptFails[1:]
			}
			s.mu.Unlock()
			if code != 0 {
				w("%s penolakan uji", strconv.Itoa(code))
				continue
			}
			to = addrOf(line[len("RCPT TO:"):])
			w("250 ok")
		case up == "DATA":
			w("354 kirim")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			s.mu.Lock()
			s.msgs = append(s.msgs, Message{From: from, To: to, Data: b.String()})
			s.mu.Unlock()
			w("250 diterima")
		case up == "QUIT":
			w("221 bye")
			return
		case up == "RSET", up == "NOOP":
			w("250 ok")
		default:
			w("502 tidak dikenal")
		}
	}
}

// addrOf mengambil alamat di antara <> (mengabaikan parameter ESMTP seperti BODY=8BITMIME).
func addrOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '>'); i >= 0 {
		s = s[:i]
	}
	return strings.Trim(s, "<> ")
}
