package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vilanobeachflorida/racksail/addons/internal/serve"
	"github.com/vilanobeachflorida/racksail/addons/ldap-sign-in/ldap"
)

// Settings are what the admin who connects the directory gives: where it
// is, how to find people in it and which of their attributes say who they
// are. The panel fills Secret in from the provider's secret.
type Settings struct {
	// URL is ldaps://host[:port], or ldap://host[:port] with StartTLS.
	URL      string `json:"url"`
	StartTLS bool   `json:"startTLS,omitempty"`
	// CACertificate is the PEM certificate of a private authority the
	// directory's certificate comes from, trusted besides the system's.
	CACertificate string `json:"caCertificate,omitempty"`

	// BindDN and Secret are the account that searches for people, for
	// directories that don't let anyone search.
	BindDN string `json:"bindDN,omitempty"`
	Secret string `json:"secret,omitempty"`

	// BaseDN, Scope and UserFilter find the person signing in; {username}
	// in the filter is what they typed, escaped.
	BaseDN     string `json:"baseDN,omitempty"`
	Scope      string `json:"scope,omitempty"`
	UserFilter string `json:"userFilter,omitempty"`
	// UserDN binds as the person directly, without searching, for
	// directories whose DNs follow from usernames, such as
	// uid={username},ou=people,dc=example,dc=com.
	UserDN string `json:"userDN,omitempty"`

	SubjectAttribute  string `json:"subjectAttribute,omitempty"`
	UsernameAttribute string `json:"usernameAttribute,omitempty"`
	NameAttribute     string `json:"nameAttribute,omitempty"`
	EmailAttribute    string `json:"emailAttribute,omitempty"`
	GroupAttribute    string `json:"groupAttribute,omitempty"`
	// RequiredGroups, when given, are the groups people must be in, by DN
	// or name, to sign in.
	RequiredGroups []string `json:"requiredGroups,omitempty"`

	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// The defaults suit OpenLDAP and 389 Directory Server.
const (
	defaultFilter   = "(uid={username})"
	defaultSubject  = "entryUUID"
	defaultUsername = "uid"
	defaultName     = "cn"
	defaultEmail    = "mail"
	defaultGroup    = "memberOf"
	defaultTimeout  = 10
	maxTimeout      = 60
	maxUsername     = 256
)

var attributePattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9-]*|[0-9]+(\.[0-9]+)+)$`)

// load reads the settings, fills in defaults and checks that they can
// work, so a mistake is reported when it is made rather than at someone's
// sign-in.
func load(raw json.RawMessage) (*Settings, error) {
	var s Settings
	if err := serve.Decode(raw, &s, "LDAP settings"); err != nil {
		return nil, err
	}
	def := func(v *string, d string) {
		if *v = strings.TrimSpace(*v); *v == "" {
			*v = d
		}
	}
	def(&s.UserFilter, defaultFilter)
	def(&s.SubjectAttribute, defaultSubject)
	def(&s.UsernameAttribute, defaultUsername)
	def(&s.NameAttribute, defaultName)
	def(&s.EmailAttribute, defaultEmail)
	def(&s.GroupAttribute, defaultGroup)
	def(&s.Scope, "sub")
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = defaultTimeout
	}

	u, err := url.Parse(s.URL)
	switch {
	case s.URL == "":
		return nil, serve.Invalid("url is the directory's address, such as ldaps://ldap.example.com")
	case err != nil || (u.Scheme != "ldap" && u.Scheme != "ldaps") || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil:
		return nil, serve.Invalid("url %q isn't an ldap:// or ldaps:// address with a host and nothing after it", s.URL)
	case u.Scheme == "ldaps" && s.StartTLS:
		return nil, serve.Invalid("startTLS is for ldap:// addresses; ldaps:// is encrypted from the start")
	case u.Scheme == "ldap" && !s.StartTLS && !loopback(u.Hostname()):
		return nil, serve.Invalid("ldap:// without startTLS would send passwords in the clear; use ldaps://, or turn on startTLS")
	case s.BindDN != "" && s.Secret == "":
		return nil, serve.Invalid("bindDN needs its password, in the provider's secret")
	case s.UserDN != "" && !strings.Contains(s.UserDN, "{username}"):
		return nil, serve.Invalid("userDN needs {username} where the username goes, such as uid={username},ou=people,dc=example,dc=com")
	case s.UserDN == "" && s.BaseDN == "":
		return nil, serve.Invalid("baseDN is where people are searched for, such as ou=people,dc=example,dc=com; or give userDN to bind without searching")
	case !strings.Contains(s.UserFilter, "{username}"):
		return nil, serve.Invalid("userFilter needs {username} where the username goes, such as %s", defaultFilter)
	case s.Scope != "sub" && s.Scope != "one":
		return nil, serve.Invalid("scope is sub, to search everything below baseDN, or one, for its children only")
	case s.TimeoutSeconds < 1 || s.TimeoutSeconds > maxTimeout:
		return nil, serve.Invalid("timeoutSeconds is from 1 to %d", maxTimeout)
	}
	if _, err := ldap.EncodeFilter(strings.ReplaceAll(s.UserFilter, "{username}", "x")); err != nil {
		return nil, serve.Invalid("userFilter: %v", err)
	}
	for name, attr := range map[string]string{
		"subjectAttribute": s.SubjectAttribute, "usernameAttribute": s.UsernameAttribute, "nameAttribute": s.NameAttribute,
		"emailAttribute": s.EmailAttribute, "groupAttribute": s.GroupAttribute,
	} {
		if !attributePattern.MatchString(attr) {
			return nil, serve.Invalid("%s %q isn't an attribute's name", name, attr)
		}
	}
	if s.CACertificate != "" {
		if _, err := pool(s.CACertificate); err != nil {
			return nil, err
		}
	}
	return &s, nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func pool(caPEM string) (*x509.CertPool, error) {
	p, err := x509.SystemCertPool()
	if err != nil {
		p = x509.NewCertPool()
	}
	if !p.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, serve.Invalid("caCertificate holds no PEM certificate")
	}
	return p, nil
}

func (s *Settings) timeout() time.Duration { return time.Duration(s.TimeoutSeconds) * time.Second }

func (s *Settings) dial(ctx context.Context) (*ldap.Conn, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if s.CACertificate != "" {
		p, err := pool(s.CACertificate)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = p
	}
	c, err := ldap.Dial(ctx, ldap.Options{URL: s.URL, StartTLS: s.StartTLS, TLS: cfg, Timeout: s.timeout()})
	if err != nil {
		return nil, serve.Failed("the directory can't be reached: %v", err)
	}
	return c, nil
}

func (s *Settings) attributes() []string {
	return []string{s.SubjectAttribute, s.UsernameAttribute, s.NameAttribute, s.EmailAttribute, s.GroupAttribute}
}

// passwordCheck is what check-password takes, and identity and answer what
// it answers, as the panel's auth.provider capability says.
type passwordCheck struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type identity struct {
	Subject  string   `json:"subject"`
	Username string   `json:"username,omitempty"`
	Name     string   `json:"name,omitempty"`
	Email    string   `json:"email,omitempty"`
	Groups   []string `json:"groups,omitempty"`
}

type answer struct {
	Identity *identity `json:"identity,omitempty"`
	Refused  bool      `json:"refused,omitempty"`
	Reason   string    `json:"reason,omitempty"`
}

func refused(reason string) answer { return answer{Refused: true, Reason: reason} }

// describe tells the panel that people sign in with a username and a
// password typed on its own screen.
func describe(ctx context.Context, settings, input json.RawMessage) (any, error) {
	if _, err := load(settings); err != nil {
		return nil, err
	}
	return map[string]string{"method": "password"}, nil
}

// checkPassword finds the person in the directory and binds as them with
// the password they typed. A wrong password, a username the directory
// doesn't know and someone outside the required groups are all refusals,
// which the panel shows alike; a directory that can't answer is a failure.
func checkPassword(ctx context.Context, settings, input json.RawMessage) (any, error) {
	s, err := load(settings)
	if err != nil {
		return nil, err
	}
	var in passwordCheck
	if err := serve.Decode(input, &in, "check"); err != nil {
		return nil, err
	}
	username := strings.TrimSpace(in.Username)
	if username == "" || in.Password == "" || len(username) > maxUsername || !utf8.ValidString(username) {
		return refused("a username and a password are needed"), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*s.timeout())
	defer cancel()
	c, err := s.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	var entry ldap.Entry
	if s.UserDN != "" {
		entry, err = s.bindDirectly(ctx, c, username, in.Password)
	} else {
		entry, err = s.searchAndBind(ctx, c, username, in.Password)
	}
	if err != nil {
		if r, ok := err.(refusal); ok {
			return refused(string(r)), nil
		}
		return nil, err
	}
	groups := texts(entry.All(s.GroupAttribute))
	if len(s.RequiredGroups) > 0 && !memberOfAny(groups, s.RequiredGroups) {
		return refused("they aren't in a group that may sign in"), nil
	}
	id := &identity{
		Subject:  subject(entry, s.SubjectAttribute),
		Username: entry.Get(s.UsernameAttribute),
		Name:     entry.Get(s.NameAttribute),
		Email:    entry.Get(s.EmailAttribute),
		Groups:   groups,
	}
	if id.Username == "" {
		id.Username = username
	}
	return answer{Identity: id}, nil
}

// refusal is a sign-in the directory turned down, as opposed to one it
// couldn't answer.
type refusal string

func (r refusal) Error() string { return string(r) }

// bindDirectly binds with the DN the username gives, then reads the
// person's own entry.
func (s *Settings) bindDirectly(ctx context.Context, c *ldap.Conn, username, password string) (ldap.Entry, error) {
	dn := strings.ReplaceAll(s.UserDN, "{username}", ldap.EscapeDN(username))
	if err := c.Bind(ctx, dn, password); err != nil {
		if ldap.IsInvalidCredentials(err) {
			return ldap.Entry{}, refusal("the directory refused the username or password")
		}
		return ldap.Entry{}, serve.Failed("binding as the person failed: %v", err)
	}
	entries, err := c.Search(ctx, dn, ldap.ScopeBase, "(objectClass=*)", s.attributes(), 1)
	switch {
	case err == nil && len(entries) == 1:
		return entries[0], nil
	case err == nil, ldap.IsResult(err, ldap.ResultInsufficientAccess), ldap.IsResult(err, ldap.ResultNoSuchObject):
		// Some directories don't let people read their own entry; the DN
		// is all there is to go on then.
		return ldap.Entry{DN: dn, Attributes: map[string][][]byte{}}, nil
	}
	return ldap.Entry{}, serve.Failed("reading the person's entry failed: %v", err)
}

// searchAndBind searches for the person, as the bind DN or anonymously,
// and binds as the one entry found.
func (s *Settings) searchAndBind(ctx context.Context, c *ldap.Conn, username, password string) (ldap.Entry, error) {
	if s.BindDN != "" {
		if err := c.Bind(ctx, s.BindDN, s.Secret); err != nil {
			if ldap.IsInvalidCredentials(err) {
				return ldap.Entry{}, serve.Failed("the directory refused bindDN's password; check bindDN and the provider's secret")
			}
			return ldap.Entry{}, serve.Failed("binding as bindDN failed: %v", err)
		}
	}
	scope := ldap.ScopeSubtree
	if s.Scope == "one" {
		scope = ldap.ScopeOne
	}
	filter := strings.ReplaceAll(s.UserFilter, "{username}", ldap.EscapeFilter(username))
	entries, err := c.Search(ctx, s.BaseDN, scope, filter, s.attributes(), 2)
	switch {
	case ldap.IsResult(err, ldap.ResultSizeLimitExceeded), err == nil && len(entries) > 1:
		return ldap.Entry{}, serve.Failed("more than one entry matches the username; make userFilter match one person only")
	case err != nil:
		return ldap.Entry{}, serve.Failed("searching for the person failed: %v", err)
	case len(entries) == 0:
		return ldap.Entry{}, refusal("the directory doesn't know the username")
	}
	if err := c.Bind(ctx, entries[0].DN, password); err != nil {
		if ldap.IsInvalidCredentials(err) {
			return ldap.Entry{}, refusal("the directory refused the username or password")
		}
		return ldap.Entry{}, serve.Failed("binding as the person failed: %v", err)
	}
	return entries[0], nil
}

// subject is what identifies the person for good: an attribute such as
// entryUUID or objectGUID, which survive renames, written as hex when it
// is binary, or else their DN.
func subject(e ldap.Entry, attr string) string {
	vs := e.All(attr)
	if len(vs) == 0 || len(vs[0]) == 0 {
		return "dn:" + strings.ToLower(e.DN)
	}
	v := vs[0]
	if utf8.Valid(v) && strings.IndexFunc(string(v), func(r rune) bool { return !unicode.IsPrint(r) }) < 0 {
		return string(v)
	}
	return hex.EncodeToString(v)
}

func texts(vs [][]byte) []string {
	var out []string
	for _, v := range vs {
		out = append(out, string(v))
	}
	return out
}

// memberOfAny reports whether one of groups, as DNs, is one of required,
// given as DNs or as the value of a group's first RDN, such as admins for
// cn=admins,ou=groups,dc=example,dc=com.
func memberOfAny(groups, required []string) bool {
	for _, g := range groups {
		name := g
		if first, _, ok := strings.Cut(g, ","); ok || strings.Contains(g, "=") {
			if _, v, ok := strings.Cut(first, "="); ok {
				name = v
			}
		}
		for _, r := range required {
			r = strings.TrimSpace(r)
			if strings.EqualFold(normalDN(g), normalDN(r)) || strings.EqualFold(strings.TrimSpace(name), r) {
				return true
			}
		}
	}
	return false
}

func normalDN(dn string) string {
	parts := strings.Split(dn, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return strings.Join(parts, ",")
}
