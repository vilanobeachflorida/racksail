// Package ldaptest runs a small directory in memory for tests. It binds
// with passwords, answers searches and switches to TLS with StartTLS, over
// a loopback address, the way a real directory does for what signing in
// asks of it.
package ldaptest

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/addons/ldap-sign-in/internal/ber"
	"github.com/vilanobeachflorida/racksail/addons/ldap-sign-in/ldap"
)

// Entry is an entry of the directory. Password, when set, is what binding
// as its DN takes.
type Entry struct {
	DN         string
	Password   string
	Attributes map[string][]string
}

// Server is a running directory.
type Server struct {
	// URL is where it listens: ldap://127.0.0.1:<port>, or ldaps:// for
	// one started with NewTLS.
	URL string
	// CA is the PEM certificate its TLS certificate is, for clients to
	// trust.
	CA []byte

	// RequireTLS refuses binds and searches before StartTLS, as
	// directories that keep passwords off the wire do.
	RequireTLS bool
	// AnonymousSearch lets connections that haven't bound search.
	AnonymousSearch bool

	tls *tls.Config
	l   net.Listener

	mu      sync.Mutex
	entries []Entry
	binds   []string
	filters []string
	wg      sync.WaitGroup
}

// New starts a directory on ldap://, with StartTLS available.
func New(t testing.TB, entries ...Entry) *Server {
	return start(t, false, entries)
}

// NewTLS starts a directory on ldaps://.
func NewTLS(t testing.TB, entries ...Entry) *Server {
	return start(t, true, entries)
}

func start(t testing.TB, ldaps bool, entries []Entry) *Server {
	t.Helper()
	cert, ca, err := certificate()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{CA: ca, tls: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, entries: entries}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	scheme := "ldap"
	if ldaps {
		l = tls.NewListener(l, s.tls)
		scheme = "ldaps"
	}
	s.l, s.URL = l, scheme+"://"+l.Addr().String()
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(func() {
		l.Close()
		s.wg.Wait()
	})
	return s
}

// Binds lists the DNs binds were asked for, whether or not they
// succeeded, in order.
func (s *Server) Binds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.binds...)
}

// Filters lists the search filters asked for, written back as RFC 4515
// text, in order.
func (s *Server) Filters() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.filters...)
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		c, err := s.l.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer c.Close()
			s.conn(c)
		}()
	}
}

type session struct {
	conn  net.Conn
	r     *bufio.Reader
	bound string
	tls   bool
}

func (s *Server) conn(c net.Conn) {
	_, encrypted := c.(*tls.Conn)
	sess := &session{conn: c, r: bufio.NewReader(c), tls: encrypted}
	for {
		c.SetDeadline(time.Now().Add(30 * time.Second))
		msg, err := ber.ReadMessage(sess.r)
		if err != nil {
			return
		}
		idContent, rest, err := ber.Expect(msg, ber.Integer)
		if err != nil {
			return
		}
		id, err := ber.DecodeInt(idContent)
		if err != nil {
			return
		}
		tag, op, _, err := ber.Next(rest)
		if err != nil {
			return
		}
		switch tag {
		case ldap.BindRequest:
			s.bind(sess, id, op)
		case ldap.SearchRequest:
			s.search(sess, id, op)
		case ldap.ExtendedRequest:
			if !s.extended(sess, id, op) {
				return
			}
		case ldap.UnbindRequest:
			return
		default:
			sess.reply(id, ber.Join(ldap.ExtendedResponse, result(ldap.ResultProtocolError, "unknown operation")...))
			return
		}
	}
}

func (sess *session) reply(id int64, op []byte) {
	sess.conn.Write(ber.Join(ber.Sequence, ber.Int(ber.Integer, id), op))
}

func result(code int, message string) [][]byte {
	return [][]byte{ber.Int(ber.Enumerated, int64(code)), ber.String(ber.OctetString, ""), ber.String(ber.OctetString, message)}
}

func (s *Server) bind(sess *session, id int64, op []byte) {
	answer := func(code int, message string) {
		sess.reply(id, ber.Join(ldap.BindResponse, result(code, message)...))
	}
	_, rest, err := ber.Expect(op, ber.Integer)
	if err != nil {
		answer(ldap.ResultProtocolError, err.Error())
		return
	}
	name, rest, err := ber.Expect(rest, ber.OctetString)
	if err != nil {
		answer(ldap.ResultProtocolError, err.Error())
		return
	}
	password, _, err := ber.Expect(rest, ldap.AuthSimple)
	if err != nil {
		answer(ldap.ResultProtocolError, "only simple binds")
		return
	}
	s.mu.Lock()
	s.binds = append(s.binds, string(name))
	s.mu.Unlock()
	if s.RequireTLS && !sess.tls {
		answer(ldap.ResultConfidentialityNeeded, "binding needs TLS")
		return
	}
	if len(name) == 0 && len(password) == 0 {
		sess.bound = ""
		answer(ldap.ResultSuccess, "")
		return
	}
	e, ok := s.find(string(name))
	if !ok || e.Password == "" || e.Password != string(password) {
		answer(ldap.ResultInvalidCredentials, "invalid credentials")
		return
	}
	sess.bound = e.DN
	answer(ldap.ResultSuccess, "")
}

func (s *Server) find(dn string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if normalDN(e.DN) == normalDN(dn) {
			return e, true
		}
	}
	return Entry{}, false
}

func normalDN(dn string) string {
	parts := strings.Split(dn, ",")
	for i, p := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(p))
	}
	return strings.Join(parts, ",")
}

// under reports whether dn is in scope of a search from base.
func under(dn, base string, scope ldap.Scope) bool {
	dn, base = normalDN(dn), normalDN(base)
	switch scope {
	case ldap.ScopeBase:
		return dn == base
	case ldap.ScopeOne:
		i := strings.IndexByte(dn, ',')
		return i >= 0 && dn[i+1:] == base
	}
	return dn == base || base == "" || strings.HasSuffix(dn, ","+base)
}

func (s *Server) search(sess *session, id int64, op []byte) {
	done := func(code int, message string) {
		sess.reply(id, ber.Join(ldap.SearchDone, result(code, message)...))
	}
	if s.RequireTLS && !sess.tls {
		done(ldap.ResultConfidentialityNeeded, "searching needs TLS")
		return
	}
	if sess.bound == "" && !s.AnonymousSearch {
		done(ldap.ResultInsufficientAccess, "bind first")
		return
	}
	base, rest, err := ber.Expect(op, ber.OctetString)
	if err != nil {
		done(ldap.ResultProtocolError, err.Error())
		return
	}
	scopeContent, rest, err := ber.Expect(rest, ber.Enumerated)
	if err != nil {
		done(ldap.ResultProtocolError, err.Error())
		return
	}
	scope, _ := ber.DecodeInt(scopeContent)
	if _, rest, err = ber.Expect(rest, ber.Enumerated); err != nil {
		done(ldap.ResultProtocolError, err.Error())
		return
	}
	limitContent, rest, err := ber.Expect(rest, ber.Integer)
	if err != nil {
		done(ldap.ResultProtocolError, err.Error())
		return
	}
	limit, _ := ber.DecodeInt(limitContent)
	if _, rest, err = ber.Expect(rest, ber.Integer); err != nil {
		done(ldap.ResultProtocolError, err.Error())
		return
	}
	if _, rest, err = ber.Expect(rest, ber.Boolean); err != nil {
		done(ldap.ResultProtocolError, err.Error())
		return
	}
	ftag, fcontent, rest, err := ber.Next(rest)
	if err != nil {
		done(ldap.ResultProtocolError, err.Error())
		return
	}
	text, err := filterText(ftag, fcontent)
	if err != nil {
		done(ldap.ResultProtocolError, err.Error())
		return
	}
	s.mu.Lock()
	s.filters = append(s.filters, text)
	entries := append([]Entry(nil), s.entries...)
	s.mu.Unlock()
	attrList, _, err := ber.Expect(rest, ber.Sequence)
	if err != nil {
		done(ldap.ResultProtocolError, err.Error())
		return
	}
	var attrs []string
	for len(attrList) > 0 {
		var a []byte
		if a, attrList, err = ber.Expect(attrList, ber.OctetString); err != nil {
			done(ldap.ResultProtocolError, err.Error())
			return
		}
		attrs = append(attrs, string(a))
	}
	if !exists(entries, string(base)) {
		done(ldap.ResultNoSuchObject, "the base of the search isn't in the directory")
		return
	}
	sent := 0
	for _, e := range entries {
		if !under(e.DN, string(base), ldap.Scope(scope)) {
			continue
		}
		ok, err := matches(ftag, fcontent, e)
		if err != nil {
			done(ldap.ResultProtocolError, err.Error())
			return
		}
		if !ok {
			continue
		}
		if limit > 0 && int64(sent) == limit {
			done(ldap.ResultSizeLimitExceeded, "")
			return
		}
		sess.reply(id, entryMessage(e, attrs))
		sent++
	}
	done(ldap.ResultSuccess, "")
}

// exists reports whether base is in the directory, or above something
// that is.
func exists(entries []Entry, base string) bool {
	for _, e := range entries {
		if under(e.DN, base, ldap.ScopeSubtree) {
			return true
		}
	}
	return base == ""
}

func entryMessage(e Entry, attrs []string) []byte {
	var list [][]byte
	for name, values := range e.Attributes {
		if len(attrs) > 0 && !containsFold(attrs, name) {
			continue
		}
		var vals [][]byte
		for _, v := range values {
			vals = append(vals, ber.String(ber.OctetString, v))
		}
		list = append(list, ber.Join(ber.Sequence, ber.String(ber.OctetString, name), ber.Join(ber.Set, vals...)))
	}
	return ber.Join(ldap.SearchEntry, ber.String(ber.OctetString, e.DN), ber.Join(ber.Sequence, list...))
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

func values(e Entry, attr string) []string {
	for k, vs := range e.Attributes {
		if strings.EqualFold(k, attr) {
			return vs
		}
	}
	return nil
}

// matches evaluates a filter, as its BER element, against an entry.
func matches(tag byte, content []byte, e Entry) (bool, error) {
	switch tag {
	case ldap.FilterAnd, ldap.FilterOr:
		found := false
		for len(content) > 0 {
			t, c, rest, err := ber.Next(content)
			if err != nil {
				return false, err
			}
			content = rest
			ok, err := matches(t, c, e)
			if err != nil {
				return false, err
			}
			if tag == ldap.FilterAnd && !ok {
				return false, nil
			}
			found = found || ok
		}
		return tag == ldap.FilterAnd || found, nil
	case ldap.FilterNot:
		t, c, _, err := ber.Next(content)
		if err != nil {
			return false, err
		}
		ok, err := matches(t, c, e)
		return !ok, err
	case ldap.FilterPresent:
		return len(values(e, string(content))) > 0, nil
	case ldap.FilterEquality, ldap.FilterApprox, ldap.FilterGreaterOrEqual, ldap.FilterLessOrEqual:
		attr, rest, err := ber.Expect(content, ber.OctetString)
		if err != nil {
			return false, err
		}
		want, _, err := ber.Expect(rest, ber.OctetString)
		if err != nil {
			return false, err
		}
		for _, v := range values(e, string(attr)) {
			cmp := strings.Compare(strings.ToLower(v), strings.ToLower(string(want)))
			switch {
			case tag == ldap.FilterGreaterOrEqual && cmp >= 0, tag == ldap.FilterLessOrEqual && cmp <= 0, cmp == 0:
				return true, nil
			}
		}
		return false, nil
	case ldap.FilterSubstrings:
		attr, rest, err := ber.Expect(content, ber.OctetString)
		if err != nil {
			return false, err
		}
		parts, _, err := ber.Expect(rest, ber.Sequence)
		if err != nil {
			return false, err
		}
		for _, v := range values(e, string(attr)) {
			if ok, err := substringsMatch(strings.ToLower(v), parts); err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("filter choice %#x", tag)
}

func substringsMatch(v string, parts []byte) (bool, error) {
	for len(parts) > 0 {
		t, c, rest, err := ber.Next(parts)
		if err != nil {
			return false, err
		}
		parts = rest
		piece := strings.ToLower(string(c))
		switch t {
		case ldap.SubstringInitial:
			if !strings.HasPrefix(v, piece) {
				return false, nil
			}
			v = v[len(piece):]
		case ldap.SubstringAny:
			i := strings.Index(v, piece)
			if i < 0 {
				return false, nil
			}
			v = v[i+len(piece):]
		case ldap.SubstringFinal:
			if !strings.HasSuffix(v, piece) {
				return false, nil
			}
			v = ""
		default:
			return false, fmt.Errorf("substring choice %#x", t)
		}
	}
	return true, nil
}

// filterText writes a filter's BER element back as RFC 4515 text, with
// values escaped, so tests can see what was asked for.
func filterText(tag byte, content []byte) (string, error) {
	var b bytes.Buffer
	b.WriteByte('(')
	switch tag {
	case ldap.FilterAnd, ldap.FilterOr, ldap.FilterNot:
		b.WriteByte(map[byte]byte{ldap.FilterAnd: '&', ldap.FilterOr: '|', ldap.FilterNot: '!'}[tag])
		for len(content) > 0 {
			t, c, rest, err := ber.Next(content)
			if err != nil {
				return "", err
			}
			content = rest
			inner, err := filterText(t, c)
			if err != nil {
				return "", err
			}
			b.WriteString(inner)
		}
	case ldap.FilterPresent:
		b.WriteString(string(content) + "=*")
	case ldap.FilterEquality, ldap.FilterApprox, ldap.FilterGreaterOrEqual, ldap.FilterLessOrEqual:
		attr, rest, err := ber.Expect(content, ber.OctetString)
		if err != nil {
			return "", err
		}
		v, _, err := ber.Expect(rest, ber.OctetString)
		if err != nil {
			return "", err
		}
		op := map[byte]string{ldap.FilterEquality: "=", ldap.FilterApprox: "~=", ldap.FilterGreaterOrEqual: ">=", ldap.FilterLessOrEqual: "<="}[tag]
		b.WriteString(string(attr) + op + ldap.EscapeFilter(string(v)))
	case ldap.FilterSubstrings:
		attr, rest, err := ber.Expect(content, ber.OctetString)
		if err != nil {
			return "", err
		}
		parts, _, err := ber.Expect(rest, ber.Sequence)
		if err != nil {
			return "", err
		}
		b.WriteString(string(attr) + "=")
		first := true
		for len(parts) > 0 {
			t, c, rest, err := ber.Next(parts)
			if err != nil {
				return "", err
			}
			parts = rest
			if t != ldap.SubstringInitial && first {
				b.WriteByte('*')
			}
			b.WriteString(ldap.EscapeFilter(string(c)))
			if t != ldap.SubstringFinal {
				b.WriteByte('*')
			}
			first = false
		}
	default:
		return "", fmt.Errorf("filter choice %#x", tag)
	}
	b.WriteByte(')')
	return b.String(), nil
}

// extended answers StartTLS and refuses anything else. It returns false
// when the connection must end.
func (s *Server) extended(sess *session, id int64, op []byte) bool {
	name, _, err := ber.Expect(op, ldap.ExtendedName)
	if err != nil || string(name) != ldap.StartTLSOID {
		sess.reply(id, ber.Join(ldap.ExtendedResponse, result(ldap.ResultProtocolError, "unsupported extended operation")...))
		return true
	}
	if sess.tls {
		sess.reply(id, ber.Join(ldap.ExtendedResponse, result(ldap.ResultProtocolError, "TLS is already on")...))
		return true
	}
	sess.reply(id, ber.Join(ldap.ExtendedResponse, result(ldap.ResultSuccess, "")...))
	tc := tls.Server(sess.conn, s.tls)
	if err := tc.Handshake(); err != nil {
		return false
	}
	sess.conn, sess.r, sess.tls = tc, bufio.NewReader(tc), true
	return true
}

// certificate makes a self-signed certificate for the loopback address.
func certificate() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "ldaptest"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"localhost"},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}
