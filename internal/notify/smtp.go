package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Connection security of a mail relay.
const (
	// SecuritySTARTTLS connects in plain text and upgrades with STARTTLS
	// before anything else is said, refusing relays that can't.
	SecuritySTARTTLS = "starttls"
	// SecurityTLS speaks TLS from the start, as on port 465.
	SecurityTLS = "tls"
	// SecurityNone never encrypts. Only a relay on the server itself is
	// given a password over it.
	SecurityNone = "none"
)

// Relay is the mail server email notifications go out through.
type Relay struct {
	Host     string
	Port     int
	Security string
	Username string
	Password string
	// From is the sender's address, and FromName the name shown with it.
	From     string
	FromName string
	// HELO is the name the panel greets the relay with; the default is
	// "localhost", which every relay accepts.
	HELO string
	// TLS, when set, replaces the default TLS settings, as for a relay with
	// its own certificate authority.
	TLS *tls.Config
	// Timeout bounds the whole conversation; the default is thirty seconds.
	Timeout time.Duration
}

// Email is one message for a relay to deliver.
type Email struct {
	To      []string
	Subject string
	Text    string
	// HTML is the same message dressed by the theme; empty sends text only.
	HTML string
	// Headers are added as they are, such as Auto-Submitted.
	Headers map[string]string
}

// security is the relay's connection security; STARTTLS unless set.
func (r *Relay) security() string {
	if r.Security == "" {
		return SecuritySTARTTLS
	}
	return r.Security
}

func (r *Relay) address() string {
	port := r.Port
	if port == 0 {
		switch r.security() {
		case SecurityTLS:
			port = 465
		case SecurityNone:
			port = 25
		default:
			port = 587
		}
	}
	return net.JoinHostPort(r.Host, strconv.Itoa(port))
}

func (r *Relay) tlsConfig() *tls.Config {
	if r.TLS != nil {
		c := r.TLS.Clone()
		if c.ServerName == "" {
			c.ServerName = r.Host
		}
		return c
	}
	return &tls.Config{ServerName: r.Host, MinVersion: tls.VersionTLS12}
}

// Send delivers an email through the relay.
func (r *Relay) Send(ctx context.Context, e Email) error {
	if len(e.To) == 0 {
		return errors.New("the email has no recipient")
	}
	from, err := mail.ParseAddress(r.From)
	if err != nil {
		return fmt.Errorf("the sender address %q isn't valid", r.From)
	}
	to := make([]string, 0, len(e.To))
	for _, a := range e.To {
		addr, err := mail.ParseAddress(a)
		if err != nil {
			return fmt.Errorf("%q isn't an email address", a)
		}
		to = append(to, addr.Address)
	}
	body, err := r.compose(from, to, e)
	if err != nil {
		return err
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := r.dial(ctx)
	if err != nil {
		return err
	}
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	// A canceled context ends the conversation at once rather than at the
	// deadline.
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()
	c, err := smtp.NewClient(conn, r.Host)
	if err != nil {
		conn.Close()
		return relayError("the relay didn't greet the panel", err)
	}
	defer c.Close()
	if err := r.converse(c, from.Address, to, body); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("the relay took too long to answer: %w", ctx.Err())
		}
		return err
	}
	return nil
}

func (r *Relay) dial(ctx context.Context) (net.Conn, error) {
	d := &net.Dialer{}
	if r.security() == SecurityTLS {
		conn, err := (&tls.Dialer{NetDialer: d, Config: r.tlsConfig()}).DialContext(ctx, "tcp", r.address())
		if err != nil {
			return nil, relayError("connecting to the relay with TLS", err)
		}
		return conn, nil
	}
	conn, err := d.DialContext(ctx, "tcp", r.address())
	if err != nil {
		return nil, relayError("connecting to the relay", err)
	}
	return conn, nil
}

func (r *Relay) converse(c *smtp.Client, from string, to []string, body []byte) error {
	helo := r.HELO
	if helo == "" {
		helo = "localhost"
	}
	if err := c.Hello(helo); err != nil {
		return relayError("greeting the relay", err)
	}
	if r.security() == SecuritySTARTTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the relay doesn't offer STARTTLS; choose TLS or no encryption in the relay settings")
		}
		if err := c.StartTLS(r.tlsConfig()); err != nil {
			return relayError("starting TLS", err)
		}
	}
	if r.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("the relay doesn't accept a user name and password")
		}
		// PlainAuth refuses to send the password unencrypted to anything but
		// the server itself.
		if err := c.Auth(smtp.PlainAuth("", r.Username, r.Password, r.Host)); err != nil {
			return relayError("signing in to the relay", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return relayError("the relay refused the sender", err)
	}
	for _, addr := range to {
		if err := c.Rcpt(addr); err != nil {
			return relayError("the relay refused "+addr, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return relayError("the relay refused the message", err)
	}
	if _, err := w.Write(body); err != nil {
		return relayError("sending the message", err)
	}
	if err := w.Close(); err != nil {
		return relayError("the relay refused the message", err)
	}
	return c.Quit()
}

// relayError keeps what the relay said, which tells people what to fix.
func relayError(what string, err error) error {
	var tp *textproto.Error
	if errors.As(err, &tp) {
		return fmt.Errorf("%s: the relay answered %d %s", what, tp.Code, tp.Msg)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// headerValue keeps a header on one line, whatever a title holds.
func headerValue(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (r *Relay) compose(from *mail.Address, to []string, e Email) ([]byte, error) {
	var buf bytes.Buffer
	sender := mail.Address{Name: headerValue(r.FromName), Address: from.Address}
	if sender.Name == "" {
		sender.Name = from.Name
	}
	domain := from.Address[strings.LastIndexByte(from.Address, '@')+1:]
	id := make([]byte, 12)
	rand.Read(id)
	headers := []string{
		"From: " + sender.String(),
		"To: " + strings.Join(to, ", "),
		"Subject: " + mime.QEncoding.Encode("utf-8", headerValue(e.Subject)),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"Message-ID: <" + hex.EncodeToString(id) + "@" + domain + ">",
		"MIME-Version: 1.0",
		// Automatic replies and vacation messages leave these alone.
		"Auto-Submitted: auto-generated",
	}
	for k, v := range e.Headers {
		headers = append(headers, textproto.CanonicalMIMEHeaderKey(headerValue(k))+": "+mime.QEncoding.Encode("utf-8", headerValue(v)))
	}
	if e.HTML == "" {
		headers = append(headers, "Content-Type: text/plain; charset=utf-8", "Content-Transfer-Encoding: quoted-printable")
		buf.WriteString(strings.Join(headers, "\r\n") + "\r\n\r\n")
		if err := writeQP(&buf, e.Text); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	mw := multipart.NewWriter(&buf)
	headers = append(headers, `Content-Type: multipart/alternative; boundary="`+mw.Boundary()+`"`)
	var out bytes.Buffer
	out.WriteString(strings.Join(headers, "\r\n") + "\r\n\r\n")
	for _, part := range []struct{ typ, body string }{{"text/plain", e.Text}, {"text/html", e.HTML}} {
		w, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.typ + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		if err := writeQP(w, part.body); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	out.Write(buf.Bytes())
	return out.Bytes(), nil
}

func writeQP(w io.Writer, s string) error {
	qp := quotedprintable.NewWriter(w)
	if _, err := qp.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n"))); err != nil {
		return err
	}
	return qp.Close()
}
