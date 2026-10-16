package ldap_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/addons/ldap-sign-in/ldap"
	"github.com/vilanobeachflorida/racksail/addons/ldap-sign-in/ldap/ldaptest"
)

var people = []ldaptest.Entry{
	{DN: "cn=reader,dc=example,dc=com", Password: "reader-secret", Attributes: map[string][]string{"objectClass": {"organizationalRole"}}},
	{DN: "uid=ada,ou=people,dc=example,dc=com", Password: "correct horse", Attributes: map[string][]string{
		"objectClass": {"person"}, "uid": {"ada"}, "cn": {"Ada Lovelace"}, "mail": {"ada@example.com"},
	}},
	{DN: "uid=alan,ou=people,dc=example,dc=com", Password: "enigma", Attributes: map[string][]string{
		"objectClass": {"person"}, "uid": {"alan"}, "cn": {"Alan Turing"},
	}},
}

func trust(t *testing.T, s *ldaptest.Server) *tls.Config {
	t.Helper()
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(s.CA) {
		t.Fatal("the test directory's certificate doesn't read")
	}
	return &tls.Config{RootCAs: p}
}

func dial(t *testing.T, o ldap.Options) *ldap.Conn {
	t.Helper()
	c, err := ldap.Dial(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestBindAndSearch(t *testing.T) {
	ctx := context.Background()
	s := ldaptest.New(t, people...)
	c := dial(t, ldap.Options{URL: s.URL, Timeout: 5 * time.Second})
	if c.Encrypted() {
		t.Error("a plain connection says it's encrypted")
	}
	if err := c.Bind(ctx, "cn=reader,dc=example,dc=com", "reader-secret"); err != nil {
		t.Fatal(err)
	}
	entries, err := c.Search(ctx, "ou=people,dc=example,dc=com", ldap.ScopeSubtree, "(&(objectClass=person)(uid=ada))", []string{"uid", "CN", "mail"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].DN != "uid=ada,ou=people,dc=example,dc=com" {
		t.Fatalf("found %+v", entries)
	}
	e := entries[0]
	if e.Get("cn") != "Ada Lovelace" || e.Get("MAIL") != "ada@example.com" || e.Get("objectClass") != "" {
		t.Errorf("the entry has %v; only what was asked for, matched without regard to case", e.Attributes)
	}
	all, err := c.Search(ctx, "dc=example,dc=com", ldap.ScopeSubtree, "(objectClass=person)", nil, 0)
	if err != nil || len(all) != 2 {
		t.Errorf("everyone: %d entries, %v", len(all), err)
	}
	one, err := c.Search(ctx, "dc=example,dc=com", ldap.ScopeOne, "(objectClass=*)", nil, 0)
	if err != nil || len(one) != 1 || one[0].DN != "cn=reader,dc=example,dc=com" {
		t.Errorf("one level: %+v, %v", one, err)
	}
	if err := c.Bind(ctx, "uid=ada,ou=people,dc=example,dc=com", "correct horse"); err != nil {
		t.Errorf("binding again as someone else: %v", err)
	}
}

func TestBindRefusesWrongPasswords(t *testing.T) {
	ctx := context.Background()
	s := ldaptest.New(t, people...)
	c := dial(t, ldap.Options{URL: s.URL})
	err := c.Bind(ctx, "uid=ada,ou=people,dc=example,dc=com", "wrong")
	if !ldap.IsInvalidCredentials(err) || !strings.Contains(err.Error(), "49") {
		t.Errorf("a wrong password: %v", err)
	}
	if err := c.Bind(ctx, "uid=nobody,ou=people,dc=example,dc=com", "x"); !ldap.IsInvalidCredentials(err) {
		t.Errorf("someone the directory doesn't have: %v", err)
	}
	if err := c.Bind(ctx, "uid=ada,ou=people,dc=example,dc=com", ""); !ldap.IsInvalidCredentials(err) {
		t.Errorf("an empty password: %v", err)
	}
	if binds := s.Binds(); len(binds) != 2 {
		t.Errorf("an empty password reached the directory: %q", binds)
	}
}

func TestStartTLS(t *testing.T) {
	ctx := context.Background()
	s := ldaptest.New(t, people...)
	s.RequireTLS = true

	plain := dial(t, ldap.Options{URL: s.URL})
	if err := plain.Bind(ctx, "cn=reader,dc=example,dc=com", "reader-secret"); !ldap.IsResult(err, ldap.ResultConfidentialityNeeded) {
		t.Errorf("binding before TLS: %v", err)
	}

	c := dial(t, ldap.Options{URL: s.URL, StartTLS: true, TLS: trust(t, s)})
	if !c.Encrypted() {
		t.Fatal("StartTLS didn't encrypt the connection")
	}
	if err := c.Bind(ctx, "cn=reader,dc=example,dc=com", "reader-secret"); err != nil {
		t.Fatal(err)
	}
	if entries, err := c.Search(ctx, "ou=people,dc=example,dc=com", ldap.ScopeSubtree, "(uid=alan)", nil, 1); err != nil || len(entries) != 1 {
		t.Errorf("searching over TLS: %v %v", entries, err)
	}

	if _, err := ldap.Dial(ctx, ldap.Options{URL: s.URL, StartTLS: true}); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Errorf("a certificate nobody trusts: %v", err)
	}
	host := strings.Replace(s.URL, "127.0.0.1", "localhost", 1)
	if _, err := ldap.Dial(ctx, ldap.Options{URL: host, StartTLS: true, TLS: &tls.Config{RootCAs: trust(t, s).RootCAs, ServerName: "ldap.example.com"}}); err == nil {
		t.Error("a certificate for another name was taken")
	}
}

func TestLDAPS(t *testing.T) {
	ctx := context.Background()
	s := ldaptest.NewTLS(t, people...)
	c := dial(t, ldap.Options{URL: s.URL, TLS: trust(t, s)})
	if !c.Encrypted() {
		t.Fatal("ldaps:// isn't encrypted")
	}
	if err := c.Bind(ctx, "uid=alan,ou=people,dc=example,dc=com", "enigma"); err != nil {
		t.Fatal(err)
	}
	if _, err := ldap.Dial(ctx, ldap.Options{URL: s.URL, Timeout: time.Second}); err == nil {
		t.Error("ldaps:// to a certificate nobody trusts")
	}
}

func TestSearchFailures(t *testing.T) {
	ctx := context.Background()
	s := ldaptest.New(t, people...)
	c := dial(t, ldap.Options{URL: s.URL})
	if _, err := c.Search(ctx, "ou=people,dc=example,dc=com", ldap.ScopeSubtree, "(uid=ada)", nil, 1); !ldap.IsResult(err, ldap.ResultInsufficientAccess) {
		t.Errorf("searching without binding: %v", err)
	}
	if err := c.Bind(ctx, "cn=reader,dc=example,dc=com", "reader-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(ctx, "ou=people,dc=example,dc=com", ldap.ScopeSubtree, "(objectClass=person)", nil, 1); !ldap.IsResult(err, ldap.ResultSizeLimitExceeded) {
		t.Errorf("more entries than the limit: %v", err)
	}
	if _, err := c.Search(ctx, "ou=nowhere,dc=example,dc=org", ldap.ScopeSubtree, "(uid=ada)", nil, 1); !ldap.IsResult(err, ldap.ResultNoSuchObject) {
		t.Errorf("a base that doesn't exist: %v", err)
	}
	if _, err := c.Search(ctx, "ou=people,dc=example,dc=com", ldap.ScopeSubtree, "uid=ada", nil, 1); err == nil {
		t.Error("a filter without parentheses was sent")
	}

	entries, err := c.Search(ctx, "ou=people,dc=example,dc=com", ldap.ScopeSubtree, "(uid="+ldap.EscapeFilter("*")+")", nil, 2)
	if err != nil || len(entries) != 0 {
		t.Errorf("an escaped * matched %v, %v", entries, err)
	}
	if got := s.Filters(); !slices.Contains(got, `(uid=\2a)`) {
		t.Errorf("the directory was asked %q", got)
	}
}

func TestDialFailures(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{"", "http://ldap.example.com", "ldap://", "ldap:///dc=example"} {
		if _, err := ldap.Dial(ctx, ldap.Options{URL: u}); err == nil {
			t.Errorf("%q was dialed", u)
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	if _, err := ldap.Dial(ctx, ldap.Options{URL: "ldap://" + addr, Timeout: time.Second}); err == nil || !strings.Contains(err.Error(), "reaching") {
		t.Errorf("nothing listening: %v", err)
	}
}
