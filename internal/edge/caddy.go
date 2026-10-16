package edge

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddytls"

	// The handlers routes are compiled to, and the apps the configuration
	// uses.
	_ "github.com/caddyserver/caddy/v2/modules/caddyevents"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/encode"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/encode/gzip"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/encode/zstd"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/fileserver"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/headers"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy/fastcgi"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/rewrite"
	_ "github.com/caddyserver/caddy/v2/modules/caddypki"
	_ "github.com/caddyserver/caddy/v2/modules/filestorage"
)

func init() {
	caddy.RegisterModule(caddyLogs{})
	caddy.RegisterModule(new(caddyRouter))
	caddy.RegisterModule(caddyPermission{})
	caddy.RegisterModule(caddyCertManager{})
	caddy.RegisterModule(caddyACMEIssuer{})
	caddy.RegisterModule(caddyLocalIssuer{})
	caddy.RegisterModule(caddyDNS{})
	caddy.RegisterModule(caddyEvents{})
}

// obj is a JSON object of Caddy's configuration.
type obj = map[string]any

func x509PoolFromPEM(data []byte) bool {
	return x509.NewCertPool().AppendCertsFromPEM(data)
}

// Issuer kinds: each issuer takes the subjects of one kind and leaves the
// others to the next issuer.
const (
	issueNames     = "names"
	issueWildcards = "wildcards"
	issueIPs       = "ips"
	issuePrivate   = "private"
	issueAny       = "any"
)

// caddyConfig is the configuration the edge runs Caddy with. Routes
// aren't in it: Racksail's router module serves them from the table, so
// changing a route never reloads Caddy.
func (e *Edge) caddyConfig() ([]byte, error) {
	s := e.Settings()
	httpAddr, httpsAddr := e.Addrs()
	httpPort, httpsPort := portOf(httpAddr), portOf(httpsAddr)

	issuers, err := e.issuers(s, httpPort, httpsPort)
	if err != nil {
		return nil, err
	}
	protocols := []string{"h1", "h2", "h3"}
	if s.DisableHTTP3 {
		protocols = protocols[:2]
	}
	routes, _ := e.table.Routes(context.Background())
	automate := keptSubjects(routes)
	if automate == nil {
		automate = []string{}
	}
	level := "INFO"
	if e.log.Enabled(context.Background(), slog.LevelDebug) {
		level = "DEBUG"
	}
	cfg := obj{
		"admin": obj{"disabled": true, "config": obj{"persist": false}},
		"logging": obj{"logs": obj{"default": obj{
			"writer": obj{"output": "discard"},
			"core":   obj{"module": "racksail", "instance": e.id},
			"level":  level,
		}}},
		"storage": obj{"module": "file_system", "root": filepath.Join(e.opts.StateDir, caddyDir)},
		"apps": obj{
			"events": obj{"subscriptions": []obj{{
				"events":   []string{"cert_obtaining", "cert_obtained", "cert_failed"},
				"handlers": []obj{{"handler": "racksail", "instance": e.id}},
			}}},
			// The local CA signs development certificates and those of
			// private names; it never touches the system's trust store.
			"pki": obj{"certificate_authorities": obj{"local": obj{"name": "Racksail Local CA", "install_trust": false}}},
			"tls": obj{
				"certificates": obj{"automate": automate},
				"automation": obj{
					"policies": []obj{{
						"issuers":         issuers,
						"get_certificate": []obj{{"via": "racksail", "instance": e.id}},
						"on_demand":       true,
						"key_type":        "p256",
					}},
					"on_demand": obj{"permission": obj{"module": "racksail", "instance": e.id}},
				},
			},
			"http": obj{
				"http_port":    httpPort,
				"https_port":   httpsPort,
				"grace_period": "15s",
				"servers": obj{
					"racksail_https": obj{
						"listen":                  []string{httpsAddr},
						"routes":                  []obj{{"handle": []obj{{"handler": "racksail", "instance": e.id}}}},
						"tls_connection_policies": []obj{{}},
						"automatic_https":         obj{"disable": true},
						"protocols":               protocols,
					},
					"racksail_http": obj{
						"listen":          []string{httpAddr},
						"routes":          []obj{{"handle": []obj{{"handler": "racksail", "instance": e.id, "plain": true}}}},
						"automatic_https": obj{"disable": true},
						"protocols":       []string{"h1", "h2c"},
					},
				},
			},
		},
	}
	return json.Marshal(cfg)
}

// issuers lists the certificate issuers in the order certmagic tries
// them. Development issues everything locally; on a server, IP addresses
// get short-lived certificates, wildcards are validated over DNS and other
// public names over HTTP or TLS-ALPN, and private names come from the
// local CA.
func (e *Edge) issuers(s Settings, httpPort, httpsPort int) ([]obj, error) {
	local := obj{"module": "racksail_local", "instance": e.id, "kind": issuePrivate}
	if e.opts.Dev {
		local["kind"] = issueAny
		return []obj{local}, nil
	}
	ca := s.CA
	if ca == "" {
		ca = LetsEncrypt
	}
	var roots []string
	if s.CARoots != "" {
		p := filepath.Join(e.opts.StateDir, "ca-roots.pem")
		if err := os.WriteFile(p, []byte(s.CARoots), 0o640); err != nil {
			return nil, err
		}
		roots = []string{p}
	}
	acme := func(kind, ca, profile string, challenges obj) obj {
		o := obj{"module": "racksail_acme", "instance": e.id, "kind": kind, "ca": ca, "challenges": challenges}
		if s.Email != "" {
			o["email"] = s.Email
		}
		if profile != "" {
			o["profile"] = profile
		}
		if len(roots) > 0 {
			o["trusted_roots_pem_files"] = roots
		}
		return o
	}
	// The challenge ports are the edge's own, which serve the challenges;
	// naming them keeps certmagic from listening on 80 or 443 itself.
	direct := obj{
		"http":     obj{"alternate_port": httpPort, "disabled": s.DisableHTTPChallenge},
		"tls-alpn": obj{"alternate_port": httpsPort, "disabled": s.DisableTLSALPNChallenge},
	}
	dns := obj{"provider": obj{"name": "racksail", "instance": e.id}}
	if len(s.DNSResolvers) > 0 {
		dns["resolvers"] = s.DNSResolvers
	}
	if s.DNSPropagationTimeout != 0 {
		dns["propagation_timeout"] = caddy.Duration(s.DNSPropagationTimeout)
	}
	profile := s.IPProfile
	if profile == "" {
		profile = "shortlived"
	}
	out := []obj{
		acme(issueIPs, ca, profile, direct),
		acme(issueWildcards, ca, "", obj{"dns": dns}),
		acme(issueNames, ca, "", direct),
	}
	if s.ZeroSSL {
		out = append(out, acme(issueNames, zeroSSL, "", direct))
	}
	return append(out, local), nil
}

var (
	_ caddyhttp.MiddlewareHandler = (*caddyRouter)(nil)
	_ caddytls.OnDemandPermission = (*caddyPermission)(nil)
)
