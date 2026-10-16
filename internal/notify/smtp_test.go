package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/events"
)

// mailServer is just enough of an SMTP server to take messages.
type mailServer struct {
	ln       net.Listener
	tls      *tls.Config
	implicit bool
	user     string
	password string

	mu   sync.Mutex
	got  []received
	auth []string
}

type received struct {
	from string
	to   []string
	data string
	tls  bool
}

func newMailServer(t *testing.T, cfg *tls.Config, implicit bool) *mailServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if implicit {
		ln = tls.NewListener(ln, cfg)
	}
	s := &mailServer{ln: ln, tls: cfg, implicit: implicit, user: "panel", password: "relay-pw"}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *mailServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *mailServer) serve(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	secure := s.implicit
	tp := textproto.NewConn(conn)
	tp.PrintfLine("220 mail.test ESMTP")
	var msg received
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO":
			lines := []string{"mail.test"}
			if s.tls != nil && !secure {
				lines = append(lines, "STARTTLS")
			}
			lines = append(lines, "AUTH PLAIN", "8BITMIME")
			for i, l := range lines {
				sep := "-"
				if i == len(lines)-1 {
					sep = " "
				}
				tp.PrintfLine("250%s%s", sep, l)
			}
		case "STARTTLS":
			tp.PrintfLine("220 go ahead")
			tc := tls.Server(conn, s.tls)
			if err := tc.Handshake(); err != nil {
				return
			}
			conn, secure = tc, true
			tp = textproto.NewConn(tc)
		case "AUTH":
			parts := strings.Fields(arg)
			raw, _ := base64.StdEncoding.DecodeString(parts[len(parts)-1])
			fields := strings.Split(string(raw), "\x00")
			s.mu.Lock()
			s.auth = append(s.auth, strings.Join(fields[1:], ":"))
			s.mu.Unlock()
			if len(fields) == 3 && fields[1] == s.user && fields[2] == s.password {
				tp.PrintfLine("235 2.7.0 accepted")
			} else {
				tp.PrintfLine("535 5.7.8 bad credentials")
			}
		case "MAIL":
			from, _, _ := strings.Cut(strings.TrimPrefix(arg, "FROM:"), " ")
			msg = received{from: strings.Trim(from, "<>"), tls: secure}
			tp.PrintfLine("250 ok")
		case "RCPT":
			to := strings.Trim(strings.TrimPrefix(arg, "TO:"), "<>")
			if strings.HasPrefix(to, "nobody@") {
				tp.PrintfLine("550 5.1.1 no such user")
				continue
			}
			msg.to = append(msg.to, to)
			tp.PrintfLine("250 ok")
		case "DATA":
			tp.PrintfLine("354 go ahead")
			data, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			msg.data = string(data)
			s.mu.Lock()
			s.got = append(s.got, msg)
			s.mu.Unlock()
			tp.PrintfLine("250 queued")
		case "QUIT":
			tp.PrintfLine("221 bye")
			return
		default:
			tp.PrintfLine("502 not here")
		}
	}
}

func (s *mailServer) messages() []received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]received(nil), s.got...)
}

// selfSigned returns a server certificate for 127.0.0.1 and a client
// configuration that trusts it.
func selfSigned(t *testing.T) (server, client *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mail.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}},
		&tls.Config{RootCAs: pool}
}

func TestRelayStartTLS(t *testing.T) {
	serverTLS, clientTLS := selfSigned(t)
	s := newMailServer(t, serverTLS, false)
	r := &Relay{Host: "127.0.0.1", Port: s.port(), Security: SecuritySTARTTLS, Username: "panel", Password: "relay-pw",
		From: "panel@example.com", FromName: "Acme\r\nBcc: evil@example.com", TLS: clientTLS}
	err := r.Send(context.Background(), Email{
		To: []string{"Ops Team <ops@example.com>"}, Subject: "Ünïcode\r\nsubject", Text: "Plain body\nline two", HTML: "<p>HTML body</p>",
		Headers: map[string]string{"X-Racksail-Event": "backups.run.failed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := s.messages()
	if len(got) != 1 || !got[0].tls || got[0].from != "panel@example.com" || len(got[0].to) != 1 || got[0].to[0] != "ops@example.com" {
		t.Fatalf("messages = %+v", got)
	}
	m, err := mail.ReadMessage(strings.NewReader(got[0].data))
	if err != nil {
		t.Fatal(err)
	}
	if m.Header.Get("Bcc") != "" || strings.Contains(m.Header.Get("From"), "\n") {
		t.Error("a name can't add headers")
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if subject != "Ünïcode subject" {
		t.Errorf("Subject = %q", subject)
	}
	if m.Header.Get("Auto-Submitted") != "auto-generated" || m.Header.Get("X-Racksail-Event") != "backups.run.failed" || m.Header.Get("Message-Id") == "" {
		t.Errorf("headers = %v", m.Header)
	}
	_, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var parts []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(p)
		parts = append(parts, p.Header.Get("Content-Type")+"|"+string(body))
	}
	if len(parts) != 2 || parts[0] != "text/plain; charset=utf-8|Plain body\nline two" || parts[1] != "text/html; charset=utf-8|<p>HTML body</p>" {
		t.Errorf("parts = %q", parts)
	}
}

func TestRelayImplicitTLSAndRefusals(t *testing.T) {
	serverTLS, clientTLS := selfSigned(t)
	s := newMailServer(t, serverTLS, true)
	r := &Relay{Host: "127.0.0.1", Port: s.port(), Security: SecurityTLS, Username: "panel", Password: "wrong", From: "panel@example.com", TLS: clientTLS}
	err := r.Send(context.Background(), Email{To: []string{"ops@example.com"}, Subject: "x", Text: "y"})
	if err == nil || !strings.Contains(err.Error(), "535") {
		t.Errorf("a refused password says what the relay answered: %v", err)
	}
	r.Password = "relay-pw"
	err = r.Send(context.Background(), Email{To: []string{"nobody@example.com"}, Subject: "x", Text: "y"})
	if err == nil || !strings.Contains(err.Error(), "nobody@example.com") || !strings.Contains(err.Error(), "550") {
		t.Errorf("a refused recipient is named: %v", err)
	}
	if err := r.Send(context.Background(), Email{To: []string{"ops@example.com"}, Subject: "x", Text: "y"}); err != nil {
		t.Fatal(err)
	}
	if got := s.messages(); len(got) != 1 || !got[0].tls {
		t.Errorf("messages = %+v", got)
	}
}

func TestRelayWithoutEncryption(t *testing.T) {
	s := newMailServer(t, nil, false)
	// STARTTLS is required unless the relay settings say otherwise.
	r := &Relay{Host: "127.0.0.1", Port: s.port(), From: "panel@example.com"}
	if err := r.Send(context.Background(), Email{To: []string{"ops@example.com"}, Text: "y"}); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("a relay without STARTTLS is refused by default: %v", err)
	}
	r.Security = SecurityNone
	r.Username, r.Password = "panel", "relay-pw"
	if err := r.Send(context.Background(), Email{To: []string{"ops@example.com"}, Subject: "plain", Text: "y"}); err != nil {
		t.Fatalf("the server's own relay takes a password in plain text: %v", err)
	}
	// Elsewhere, a password never goes out unencrypted: the same server
	// written another way isn't known to be this one.
	r.Host = "::ffff:127.0.0.1"
	if err := r.Send(context.Background(), Email{To: []string{"ops@example.com"}, Text: "y"}); err == nil || !strings.Contains(err.Error(), "unencrypted") {
		t.Errorf("a password is refused to an unencrypted relay that isn't the server itself: %v", err)
	}
	if n := len(s.messages()); n != 1 {
		t.Errorf("%d messages arrived, want 1", n)
	}
}

func TestSendEmailThroughService(t *testing.T) {
	s := newMailServer(t, nil, false)
	env := Env{
		BrandName: "Acme Hosting",
		Relay: func(context.Context) (*Relay, error) {
			return &Relay{Host: "127.0.0.1", Port: s.port(), Security: SecurityNone, From: "alerts@acme.example"}, nil
		},
	}
	ch, err := New("email", settings(EmailSettings{To: []string{"ops@acme.example", "oncall@acme.example"}}), env)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(context.Background(), Message{Title: "Site down", Body: "shop.example.com answers 502.", Severity: events.SeverityCritical, URL: "https://panel.acme.example/"}); err != nil {
		t.Fatal(err)
	}
	got := s.messages()
	if len(got) != 1 || len(got[0].to) != 2 {
		t.Fatalf("messages = %+v", got)
	}
	m, _ := mail.ReadMessage(strings.NewReader(got[0].data))
	if subject, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject")); subject != "Critical: Site down" {
		t.Errorf("Subject = %q", subject)
	}
	body, _ := io.ReadAll(bufio.NewReader(m.Body))
	if !strings.Contains(string(body), "Acme Hosting") {
		t.Error("the HTML part carries the brand")
	}
}

func TestRelayAddress(t *testing.T) {
	for sec, port := range map[string]int{SecuritySTARTTLS: 587, SecurityTLS: 465, SecurityNone: 25, "": 587} {
		r := &Relay{Host: "smtp.example.com", Security: sec}
		if got := r.address(); got != "smtp.example.com:"+strconv.Itoa(port) {
			t.Errorf("%q: %s", sec, got)
		}
	}
}
