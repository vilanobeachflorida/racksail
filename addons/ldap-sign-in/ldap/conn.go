// Package ldap is the part of LDAPv3 (RFC 4511) that signing people in
// needs: binding with a password, searching for an entry and StartTLS,
// with search filters as RFC 4515 writes them.
package ldap

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/addons/ldap-sign-in/internal/ber"
)

// Protocol operations (RFC 4511, section 4.2 onwards), as the tags of their
// elements.
const (
	BindRequest      = ber.Application | ber.Constructed // tag number 0
	BindResponse     = ber.Application | ber.Constructed | 1
	UnbindRequest    = ber.Application | 2
	SearchRequest    = ber.Application | ber.Constructed | 3
	SearchEntry      = ber.Application | ber.Constructed | 4
	SearchDone       = ber.Application | ber.Constructed | 5
	SearchReference  = ber.Application | ber.Constructed | 19
	ExtendedRequest  = ber.Application | ber.Constructed | 23
	ExtendedResponse = ber.Application | ber.Constructed | 24

	// AuthSimple is the choice of a bind with a password, [0].
	AuthSimple = ber.Context
	// ExtendedName is the OID of an extended request, [0].
	ExtendedName = ber.Context
)

// StartTLSOID names the StartTLS extended operation (RFC 4511, section
// 4.14).
const StartTLSOID = "1.3.6.1.4.1.1466.20037"

// Result codes (RFC 4511, appendix A).
const (
	ResultSuccess               = 0
	ResultProtocolError         = 2
	ResultSizeLimitExceeded     = 4
	ResultConfidentialityNeeded = 13
	ResultNoSuchObject          = 32
	ResultInvalidCredentials    = 49
	ResultInsufficientAccess    = 50
	ResultUnwillingToPerform    = 53
)

const (
	defaultTimeout = 10 * time.Second
	// MaxEntries bounds what one search returns, whatever limit it asks
	// for.
	MaxEntries = 1000
	// noticeID is the message ID of what a server sends unasked.
	noticeID = 0
)

// ResultError is an operation the server refused, with its result code and
// what it said.
type ResultError struct {
	Op      string
	Code    int
	Message string
}

func (e *ResultError) Error() string {
	msg := fmt.Sprintf("ldap: %s failed with result %d", e.Op, e.Code)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// IsResult reports whether err is an operation refused with code.
func IsResult(err error, code int) bool {
	var re *ResultError
	return errors.As(err, &re) && re.Code == code
}

// IsInvalidCredentials reports whether err is a bind the server refused
// for its password.
func IsInvalidCredentials(err error) bool { return IsResult(err, ResultInvalidCredentials) }

// Scope is how far a search reaches below its base.
type Scope int

// Search scopes.
const (
	ScopeBase Scope = iota
	ScopeOne
	ScopeSubtree
)

// Entry is a directory entry a search found.
type Entry struct {
	DN         string
	Attributes map[string][][]byte
}

// Get returns an attribute's first value, matching its name without regard
// to case.
func (e Entry) Get(name string) string {
	if vs := e.All(name); len(vs) > 0 {
		return string(vs[0])
	}
	return ""
}

// All returns every value of an attribute.
func (e Entry) All(name string) [][]byte {
	for k, vs := range e.Attributes {
		if strings.EqualFold(k, name) {
			return vs
		}
	}
	return nil
}

// Options say how to reach a directory.
type Options struct {
	// URL is ldap://host[:389] or ldaps://host[:636].
	URL string
	// StartTLS switches an ldap:// connection to TLS before anything else
	// is sent over it.
	StartTLS bool
	TLS      *tls.Config
	// Timeout bounds connecting and each operation.
	Timeout time.Duration
}

// Conn is a connection to a directory, for one goroutine at a time.
type Conn struct {
	conn    net.Conn
	r       *bufio.Reader
	id      int64
	timeout time.Duration
}

// Dial connects to a directory and, for ldaps:// or with StartTLS, makes
// sure the connection is encrypted before it returns.
func Dial(ctx context.Context, o Options) (*Conn, error) {
	u, err := url.Parse(o.URL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "ldap" && u.Scheme != "ldaps") {
		return nil, fmt.Errorf("ldap: %q isn't an ldap:// or ldaps:// address", o.URL)
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"ldap": "389", "ldaps": "636"}[u.Scheme]
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	cfg := &tls.Config{}
	if o.TLS != nil {
		cfg = o.TLS.Clone()
	}
	if cfg.ServerName == "" {
		cfg.ServerName = u.Hostname()
	}
	if cfg.MinVersion == 0 {
		cfg.MinVersion = tls.VersionTLS12
	}
	d := &net.Dialer{Timeout: timeout}
	raw, err := d.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return nil, fmt.Errorf("ldap: reaching %s: %w", u.Host, err)
	}
	c := &Conn{conn: raw, r: bufio.NewReader(raw), timeout: timeout}
	switch {
	case u.Scheme == "ldaps":
		err = c.handshake(ctx, cfg)
	case o.StartTLS:
		err = c.startTLS(ctx, cfg)
	}
	if err != nil {
		raw.Close()
		return nil, err
	}
	return c, nil
}

func (c *Conn) handshake(ctx context.Context, cfg *tls.Config) error {
	tc := tls.Client(c.conn, cfg)
	hctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		return fmt.Errorf("ldap: the directory's TLS: %w", err)
	}
	c.conn, c.r = tc, bufio.NewReader(tc)
	return nil
}

// startTLS asks the server to switch the connection to TLS, then does the
// handshake.
func (c *Conn) startTLS(ctx context.Context, cfg *tls.Config) error {
	op, err := c.roundTrip(ctx, ber.Join(ExtendedRequest, ber.String(ExtendedName, StartTLSOID)), ExtendedResponse)
	if err != nil {
		return err
	}
	res, err := parseResult(op)
	if err != nil {
		return err
	}
	if res.code != ResultSuccess {
		return &ResultError{Op: "StartTLS", Code: res.code, Message: res.message}
	}
	// Anything already read past the response came before the handshake,
	// unencrypted.
	if c.r.Buffered() > 0 {
		return errors.New("ldap: the server sent more before TLS started")
	}
	return c.handshake(ctx, cfg)
}

// Encrypted reports whether TLS protects the connection.
func (c *Conn) Encrypted() bool {
	_, ok := c.conn.(*tls.Conn)
	return ok
}

// Bind authenticates the connection as dn with a password. An empty
// password is refused here, without asking the server: servers take it as
// an unauthenticated bind, which succeeds whatever the name.
func (c *Conn) Bind(ctx context.Context, dn, password string) error {
	if password == "" {
		return &ResultError{Op: "bind", Code: ResultInvalidCredentials, Message: "an empty password signs nobody in"}
	}
	req := ber.Join(BindRequest, ber.Int(ber.Integer, 3), ber.String(ber.OctetString, dn), ber.String(AuthSimple, password))
	op, err := c.roundTrip(ctx, req, BindResponse)
	if err != nil {
		return err
	}
	res, err := parseResult(op)
	if err != nil {
		return err
	}
	if res.code != ResultSuccess {
		return &ResultError{Op: "bind", Code: res.code, Message: res.message}
	}
	return nil
}

// Search finds the entries filter matches from base, with the values of
// attrs. A search that matches more than limit entries fails with
// ResultSizeLimitExceeded.
func (c *Conn) Search(ctx context.Context, base string, scope Scope, filter string, attrs []string, limit int) ([]Entry, error) {
	f, err := EncodeFilter(filter)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxEntries {
		limit = MaxEntries
	}
	var list [][]byte
	for _, a := range attrs {
		list = append(list, ber.String(ber.OctetString, a))
	}
	req := ber.Join(SearchRequest,
		ber.String(ber.OctetString, base), ber.Int(ber.Enumerated, int64(scope)),
		ber.Int(ber.Enumerated, 0), // never dereference aliases
		ber.Int(ber.Integer, int64(limit)), ber.Int(ber.Integer, int64(c.timeout/time.Second)),
		ber.Bool(false), f, ber.Join(ber.Sequence, list...))
	id, err := c.send(ctx, req)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for {
		tag, op, err := c.receive(ctx, id)
		if err != nil {
			return nil, err
		}
		switch tag {
		case SearchEntry:
			if len(entries) == limit {
				return nil, &ResultError{Op: "search", Code: ResultSizeLimitExceeded, Message: "the server sent more entries than the search asked for"}
			}
			e, err := parseEntry(op)
			if err != nil {
				return nil, err
			}
			entries = append(entries, e)
		case SearchReference:
			// Referrals point at other servers, which signing in doesn't
			// follow.
		case SearchDone:
			res, err := parseResult(op)
			if err != nil {
				return nil, err
			}
			if res.code != ResultSuccess {
				return nil, &ResultError{Op: "search", Code: res.code, Message: res.message}
			}
			return entries, nil
		default:
			return nil, fmt.Errorf("%w: a search answered with operation %#x", ber.ErrMalformed, tag)
		}
	}
}

// Close says goodbye to the server and closes the connection.
func (c *Conn) Close() error {
	c.conn.SetWriteDeadline(time.Now().Add(time.Second))
	c.id++
	c.conn.Write(ber.Join(ber.Sequence, ber.Int(ber.Integer, c.id), []byte{UnbindRequest, 0}))
	return c.conn.Close()
}

// roundTrip sends one request and reads its one response, which has the
// tag want.
func (c *Conn) roundTrip(ctx context.Context, req []byte, want byte) ([]byte, error) {
	id, err := c.send(ctx, req)
	if err != nil {
		return nil, err
	}
	tag, op, err := c.receive(ctx, id)
	if err != nil {
		return nil, err
	}
	if tag != want {
		return nil, fmt.Errorf("%w: expected operation %#x, got %#x", ber.ErrMalformed, want, tag)
	}
	return op, nil
}

func (c *Conn) deadline(ctx context.Context) time.Time {
	d := time.Now().Add(c.timeout)
	if t, ok := ctx.Deadline(); ok && t.Before(d) {
		return t
	}
	return d
}

func (c *Conn) send(ctx context.Context, op []byte) (int64, error) {
	c.id++
	c.conn.SetWriteDeadline(c.deadline(ctx))
	if _, err := c.conn.Write(ber.Join(ber.Sequence, ber.Int(ber.Integer, c.id), op)); err != nil {
		return 0, fmt.Errorf("ldap: sending a request: %w", err)
	}
	return c.id, nil
}

// receive reads the next message, which answers request id. The only
// message a server sends unasked is the notice that it is disconnecting.
func (c *Conn) receive(ctx context.Context, id int64) (byte, []byte, error) {
	c.conn.SetReadDeadline(c.deadline(ctx))
	content, err := ber.ReadMessage(c.r)
	if err != nil {
		return 0, nil, fmt.Errorf("ldap: reading the server's answer: %w", err)
	}
	idContent, rest, err := ber.Expect(content, ber.Integer)
	if err != nil {
		return 0, nil, err
	}
	got, err := ber.DecodeInt(idContent)
	if err != nil {
		return 0, nil, err
	}
	tag, op, _, err := ber.Next(rest)
	if err != nil {
		return 0, nil, err
	}
	if got == noticeID && tag == ExtendedResponse {
		res, _ := parseResult(op)
		return 0, nil, &ResultError{Op: "the connection", Code: res.code, Message: "the server is disconnecting: " + res.message}
	}
	if got != id {
		return 0, nil, fmt.Errorf("%w: an answer to request %d, not %d", ber.ErrMalformed, got, id)
	}
	return tag, op, nil
}

type result struct {
	code    int
	message string
}

// parseResult reads the LDAPResult every response starts with.
func parseResult(op []byte) (result, error) {
	code, rest, err := ber.Expect(op, ber.Enumerated)
	if err != nil {
		return result{}, err
	}
	n, err := ber.DecodeInt(code)
	if err != nil {
		return result{}, err
	}
	if _, rest, err = ber.Expect(rest, ber.OctetString); err != nil {
		return result{}, err
	}
	msg, _, err := ber.Expect(rest, ber.OctetString)
	if err != nil {
		return result{}, err
	}
	return result{code: int(n), message: string(msg)}, nil
}

func parseEntry(op []byte) (Entry, error) {
	dn, rest, err := ber.Expect(op, ber.OctetString)
	if err != nil {
		return Entry{}, err
	}
	attrs, _, err := ber.Expect(rest, ber.Sequence)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{DN: string(dn), Attributes: map[string][][]byte{}}
	for len(attrs) > 0 {
		var attr []byte
		if attr, attrs, err = ber.Expect(attrs, ber.Sequence); err != nil {
			return Entry{}, err
		}
		name, rest, err := ber.Expect(attr, ber.OctetString)
		if err != nil {
			return Entry{}, err
		}
		vals, _, err := ber.Expect(rest, ber.Set)
		if err != nil {
			return Entry{}, err
		}
		var values [][]byte
		for len(vals) > 0 {
			var v []byte
			if v, vals, err = ber.Expect(vals, ber.OctetString); err != nil {
				return Entry{}, err
			}
			values = append(values, v)
		}
		e.Attributes[string(name)] = values
	}
	return e, nil
}

// EscapeDN escapes a value for a distinguished name (RFC 4514, section
// 2.4), as when a bind DN is written from a username.
func EscapeDN(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case strings.IndexByte(`,+"\<>;=`, c) >= 0:
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == 0:
			b.WriteString(`\00`)
		case (c == ' ' || c == '#') && i == 0, c == ' ' && i == len(value)-1:
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
