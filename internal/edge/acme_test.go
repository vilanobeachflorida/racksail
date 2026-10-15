package edge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letsencrypt/pebble/v2/ca"
	"github.com/letsencrypt/pebble/v2/db"
	"github.com/letsencrypt/pebble/v2/va"
	"github.com/letsencrypt/pebble/v2/wfe"
	"github.com/miekg/dns"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// testZone is the zone the ACME test's names are in. Its DNS server sends
// every name to the loopback address, where the edge listens.
const testZone = "racksail.test."

// testDNS is the zone's authoritative server, which the CA validates
// against and the DNS-01 records are published in.
type testDNS struct {
	mu   sync.Mutex
	txt  map[string]string
	addr string
}

func startDNS(t *testing.T) *testDNS {
	t.Helper()
	d := &testDNS{txt: map[string]string{}}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	d.addr = tcp.Addr().String()
	handler := dns.HandlerFunc(d.serve)
	servers := []*dns.Server{{Listener: tcp, Handler: handler}, {PacketConn: udp, Handler: handler}}
	for _, s := range servers {
		go s.ActivateAndServe()
	}
	t.Cleanup(func() {
		for _, s := range servers {
			s.Shutdown()
		}
	})
	return d
}

func (d *testDNS) serve(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	soa := &dns.SOA{
		Hdr: dns.RR_Header{Name: testZone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 60},
		Ns:  "ns." + testZone, Mbox: "admin." + testZone, Serial: 1, Refresh: 60, Retry: 60, Expire: 600, Minttl: 60,
	}
	for _, q := range r.Question {
		name := strings.ToLower(q.Name)
		if name != testZone && !strings.HasSuffix(name, "."+testZone) {
			m.Rcode = dns.RcodeRefused
			continue
		}
		hdr := dns.RR_Header{Name: q.Name, Rrtype: q.Qtype, Class: dns.ClassINET, Ttl: 1}
		switch q.Qtype {
		case dns.TypeSOA:
			if name == testZone {
				m.Answer = append(m.Answer, soa)
			} else {
				m.Ns = append(m.Ns, soa)
			}
		case dns.TypeA:
			m.Answer = append(m.Answer, &dns.A{Hdr: hdr, A: net.ParseIP("127.0.0.1")})
		case dns.TypeTXT:
			d.mu.Lock()
			if v, ok := d.txt[name]; ok {
				m.Answer = append(m.Answer, &dns.TXT{Hdr: hdr, Txt: []string{v}})
			}
			d.mu.Unlock()
		default:
			m.Ns = append(m.Ns, soa)
		}
	}
	w.WriteMsg(m)
}

// dnsPanel is the panel's DNS module for the test zone.
type dnsPanel struct{ dns *testDNS }

func (p dnsPanel) AllowCertificate(ctx context.Context, name string) error { return nil }

func (p dnsPanel) PresentRecord(ctx context.Context, zone string, rec DNSRecord) error {
	p.dns.mu.Lock()
	defer p.dns.mu.Unlock()
	p.dns.txt[strings.ToLower(rec.Name+"."+zone+".")] = rec.Value
	return nil
}

func (p dnsPanel) CleanUpRecord(ctx context.Context, zone string, rec DNSRecord) error {
	p.dns.mu.Lock()
	defer p.dns.mu.Unlock()
	delete(p.dns.txt, strings.ToLower(rec.Name+"."+zone+"."))
	return nil
}

// startPebble runs Let's Encrypt's test CA in the process, validating
// challenges on the edge's ports and resolving names with the test zone's
// server.
func startPebble(t *testing.T, httpPort, tlsPort int, resolver string) (directory string, roots string) {
	t.Helper()
	t.Setenv("PEBBLE_VA_NOSLEEP", "1")
	t.Setenv("PEBBLE_WFE_NONCEREJECT", "0")
	logger := log.New(io.Discard, "", 0)
	store := db.NewMemoryStore()
	profiles := map[string]ca.Profile{
		"default":    {Description: "The default profile"},
		"shortlived": {Description: "Six days", ValidityPeriod: 160 * 3600},
	}
	authority := ca.New(logger, store, "", "ecdsa", 0, 1, profiles)
	validator := va.New(logger, httpPort, tlsPort, false, resolver, store)
	frontend := wfe.New(logger, store, validator, authority, []string{"pebble.letsencrypt.org"}, false, false, 0, 0)
	ts := httptest.NewTLSServer(frontend.Handler())
	t.Cleanup(ts.Close)
	return ts.URL + wfe.DirectoryPath, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}))
}

func TestACMECertificates(t *testing.T) {
	if testing.Short() {
		t.Skip("issues certificates from a test CA")
	}
	ctx := context.Background()
	httpAddr, err := resolveAddr("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpsAddr, err := resolveAddr("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	zone := startDNS(t)
	directory, roots := startPebble(t, portOf(httpAddr), portOf(httpsAddr), zone.addr)

	te := startEdge(t, Options{
		HTTP: httpAddr, HTTPS: httpsAddr, Responder: dnsPanel{dns: zone},
		Settings: Settings{
			Email: "admin@racksail.test", CA: directory, CARoots: roots,
			DNSResolvers: []string{zone.addr}, DNSPropagationTimeout: -1,
		},
	})
	// Loopback addresses stand in for the server's public address.
	te.public = func(string) bool { return true }

	owner := store.NewID("site")
	if err := te.admin.SetRoutes(ctx, owner, []Route{
		{ID: "app", Hosts: []string{"app.racksail.test"}, Handler: Handler{Type: HandleRespond, Body: "app"}},
		{ID: "wild", Hosts: []string{"*.racksail.test"}, Handler: Handler{Type: HandleRespond, Body: "wild"}},
		{ID: "ip", Hosts: []string{"127.0.0.1"}, Handler: Handler{Type: HandleRespond, Body: "ip"}},
		{ID: "demand", Hosts: []string{"a.demand.racksail.test"}, TLS: TLS{Mode: TLSOnDemand}, Handler: Handler{Type: HandleRespond, Body: "demand"}},
	}); err != nil {
		t.Fatal(err)
	}
	te.waitRunning(t, true)

	// The keeper obtains the auto routes' certificates on its own.
	var certs []Certificate
	deadline := time.Now().Add(90 * time.Second)
	for {
		certs = te.Certificates(ctx)
		valid := 0
		for _, c := range certs {
			if c.Status == CertValid {
				valid++
			}
		}
		if valid == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("certificates = %+v", certs)
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, c := range certs {
		if c.Source != "acme" || !strings.Contains(c.Issuer, "Pebble") {
			t.Errorf("%s: %+v", c.Subject, c)
		}
	}

	handshake := func(serverName string) *x509.Certificate {
		t.Helper()
		conn, err := tls.Dial("tcp", httpsAddr, &tls.Config{ServerName: serverName, InsecureSkipVerify: true})
		if err != nil {
			t.Fatalf("handshake for %q: %v", serverName, err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0]
	}
	if leaf := handshake("app.racksail.test"); len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "app.racksail.test" {
		t.Errorf("app = %v", leaf.DNSNames)
	}
	// Names under the wildcard share its certificate, validated over DNS.
	if leaf := handshake("shop.racksail.test"); len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "*.racksail.test" {
		t.Errorf("wildcard = %v", leaf.DNSNames)
	}
	// A client reaching the IP address sends no name; the certificate is
	// for the address, and short-lived.
	ip := handshake("")
	if len(ip.IPAddresses) != 1 || !ip.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")) {
		t.Errorf("IP certificate = %v", ip.IPAddresses)
	} else if life := ip.NotAfter.Sub(ip.NotBefore); life > 7*24*time.Hour {
		t.Errorf("an IP certificate should be short-lived, not %s", life)
	}
	// On-demand certificates are obtained at the first handshake.
	if leaf := handshake("a.demand.racksail.test"); len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "a.demand.racksail.test" || !strings.Contains(leaf.Issuer.CommonName, "Pebble") {
		t.Errorf("on demand = %v by %s", leaf.DNSNames, leaf.Issuer.CommonName)
	}
}
