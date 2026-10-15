package edge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddytls"
	"github.com/caddyserver/certmagic"
	"github.com/libdns/libdns"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// CertSource provides uploaded certificates by name.
type CertSource interface {
	Certificate(name string) (*tls.Certificate, error)
}

// FileCerts loads certificates from <dir>/<name>/fullchain.pem and
// privkey.pem, as the agent installs them.
type FileCerts struct {
	Dir string
}

// Certificate implements CertSource.
func (f FileCerts) Certificate(name string) (*tls.Certificate, error) {
	if f.Dir == "" {
		return nil, fmt.Errorf("certificate %s: no certificates are installed: %w", name, fs.ErrNotExist)
	}
	if !certPattern.MatchString(name) {
		return nil, fmt.Errorf("certificate name %q", name)
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(f.Dir, name, "fullchain.pem"), filepath.Join(f.Dir, name, "privkey.pem"))
	if err != nil {
		return nil, err
	}
	return &cert, nil
}

// certTTL is how long a loaded certificate is used before it is read
// again, so a replaced certificate is picked up without a restart.
const certTTL = 30 * time.Second

// cachedCerts keeps the uploaded certificates handshakes need.
type cachedCerts struct {
	src CertSource
	mu  sync.Mutex
	m   map[string]cachedCert
	now func() time.Time
}

type cachedCert struct {
	cert   *tls.Certificate
	err    error
	loaded time.Time
}

func newCachedCerts(src CertSource) *cachedCerts {
	return &cachedCerts{src: src, m: map[string]cachedCert{}, now: time.Now}
}

func (c *cachedCerts) get(name string) (*tls.Certificate, error) {
	c.mu.Lock()
	entry, ok := c.m[name]
	c.mu.Unlock()
	if ok && c.now().Sub(entry.loaded) < certTTL {
		return entry.cert, entry.err
	}
	cert, err := c.src.Certificate(name)
	if err == nil && cert.Leaf == nil && len(cert.Certificate) > 0 {
		cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
	}
	c.mu.Lock()
	c.m[name] = cachedCert{cert: cert, err: err, loaded: c.now()}
	c.mu.Unlock()
	return cert, err
}

// caddyPermission decides which names may have on-demand certificates:
// the names routes serve, and for routes that ask, the names the panel
// approves.
type caddyPermission struct {
	Instance string `json:"instance"`
	edge     *Edge
}

// CaddyModule implements caddy.Module.
func (caddyPermission) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "tls.permission.racksail", New: func() caddy.Module { return new(caddyPermission) }}
}

// Provision implements caddy.Provisioner.
func (p *caddyPermission) Provision(ctx caddy.Context) error {
	e, err := lookup(p.Instance)
	p.edge = e
	return err
}

// CertificateAllowed implements caddytls.OnDemandPermission.
func (p *caddyPermission) CertificateAllowed(ctx context.Context, name string) error {
	return p.edge.allowCertificate(ctx, name)
}

func (e *Edge) allowCertificate(ctx context.Context, name string) error {
	route, ok := e.table.Match(name, "/")
	if !ok {
		return fmt.Errorf("%w: no route serves %s", caddytls.ErrPermissionDenied, name)
	}
	if route.Passive && e.table.Active() == 0 {
		return fmt.Errorf("%w: %s isn't served now", caddytls.ErrPermissionDenied, name)
	}
	switch route.tlsMode() {
	case TLSOff, TLSCertificate:
		return fmt.Errorf("%w: %s doesn't use automatic certificates", caddytls.ErrPermissionDenied, name)
	}
	if !route.TLS.Ask {
		return nil
	}
	// Anyone can send a handshake for any name, so refusals are remembered
	// for a while and only so many questions are open at once.
	if e.asked.refused(name) {
		return fmt.Errorf("%w: the panel refused %s recently", caddytls.ErrPermissionDenied, name)
	}
	release, ok := e.asked.acquire()
	if !ok {
		return fmt.Errorf("too many certificate checks are waiting for the panel; %s is refused for now", name)
	}
	defer release()
	err := e.ask(ctx, Question{Kind: AskCertificate, Name: name})
	if errors.Is(err, ErrDenied) {
		e.asked.refuse(name)
		return fmt.Errorf("%w: %v", caddytls.ErrPermissionDenied, err)
	}
	return err
}

// Limits on asking the panel about names.
const (
	maxOpenAsks     = 64
	refusalMemory   = 10 * time.Minute
	maxRefusalsKept = 10000
)

// askLimiter bounds the questions handshakes put to the panel.
type askLimiter struct {
	open chan struct{}
	mu   sync.Mutex
	no   map[string]time.Time
	now  func() time.Time
}

func newAskLimiter() *askLimiter {
	return &askLimiter{open: make(chan struct{}, maxOpenAsks), no: map[string]time.Time{}, now: time.Now}
}

func (a *askLimiter) acquire() (release func(), ok bool) {
	select {
	case a.open <- struct{}{}:
		return func() { <-a.open }, true
	default:
		return nil, false
	}
}

func (a *askLimiter) refused(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	until, ok := a.no[name]
	if ok && a.now().After(until) {
		delete(a.no, name)
		return false
	}
	return ok
}

func (a *askLimiter) refuse(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.no) >= maxRefusalsKept {
		a.no = map[string]time.Time{}
	}
	a.no[name] = a.now().Add(refusalMemory)
}

// caddyCertManager hands out uploaded certificates during handshakes.
type caddyCertManager struct {
	Instance string `json:"instance"`
	edge     *Edge
}

// CaddyModule implements caddy.Module.
func (caddyCertManager) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "tls.get_certificate.racksail", New: func() caddy.Module { return new(caddyCertManager) }}
}

// Provision implements caddy.Provisioner.
func (m *caddyCertManager) Provision(ctx caddy.Context) error {
	e, err := lookup(m.Instance)
	m.edge = e
	return err
}

// GetCertificate implements certmagic.Manager. It answers nil for names
// that don't use uploaded certificates, so the issued ones are used.
func (m *caddyCertManager) GetCertificate(ctx context.Context, hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	route, ok := m.edge.table.Match(hello.ServerName, "/")
	if !ok || route.tlsMode() != TLSCertificate {
		return nil, nil
	}
	return m.edge.certs.get(route.TLS.Certificate)
}

// caddyACMEIssuer is Caddy's ACME issuer for one kind of subject. The
// issuers of a policy are tried in order and each one declines the
// subjects that aren't its kind, so IP addresses, wildcards and other
// names each get the CA settings and challenges they need.
type caddyACMEIssuer struct {
	*caddytls.ACMEIssuer
	Instance string `json:"instance"`
	Kind     string `json:"kind"`
	edge     *Edge
}

// CaddyModule implements caddy.Module.
func (caddyACMEIssuer) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "tls.issuance.racksail_acme", New: func() caddy.Module {
		return &caddyACMEIssuer{ACMEIssuer: new(caddytls.ACMEIssuer)}
	}}
}

// Provision implements caddy.Provisioner.
func (iss *caddyACMEIssuer) Provision(ctx caddy.Context) error {
	e, err := lookup(iss.Instance)
	if err != nil {
		return err
	}
	iss.edge = e
	switch iss.Kind {
	case issueNames, issueWildcards, issueIPs:
	default:
		return fmt.Errorf("issuer kind %q", iss.Kind)
	}
	return iss.ACMEIssuer.Provision(ctx)
}

// SetConfig implements caddytls.ConfigSetter, and lets the edge obtain
// certificates ahead of visitors with the same configuration.
func (iss *caddyACMEIssuer) SetConfig(cfg *certmagic.Config) {
	iss.ACMEIssuer.SetConfig(cfg)
	iss.edge.setTLS(nil, cfg)
}

// takes reports whether a subject is one this issuer's kind takes.
func (e *Edge) takes(kind, name string) bool {
	public := certmagic.SubjectQualifiesForPublicCert(name)
	if e.public != nil {
		public = e.public(name)
	}
	switch kind {
	case issueAny:
		return true
	case issuePrivate:
		return !public
	case issueIPs:
		return certmagic.SubjectIsIP(name) && public
	case issueWildcards:
		return strings.HasPrefix(name, "*.") && public
	case issueNames:
		return !certmagic.SubjectIsIP(name) && !strings.Contains(name, "*") && public
	}
	return false
}

// PreCheck implements certmagic.PreChecker.
func (iss *caddyACMEIssuer) PreCheck(ctx context.Context, names []string, interactive bool) error {
	for _, n := range names {
		if !iss.edge.takes(iss.Kind, n) {
			return fmt.Errorf("the %s issuer doesn't take %s", iss.Kind, n)
		}
	}
	return iss.ACMEIssuer.PreCheck(ctx, names, interactive)
}

// caddyLocalIssuer is Caddy's internal issuer, the edge's local CA, for
// development and for names no public CA certifies.
type caddyLocalIssuer struct {
	*caddytls.InternalIssuer
	Instance string `json:"instance"`
	Kind     string `json:"kind"`
	edge     *Edge
}

// CaddyModule implements caddy.Module.
func (caddyLocalIssuer) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "tls.issuance.racksail_local", New: func() caddy.Module {
		return &caddyLocalIssuer{InternalIssuer: new(caddytls.InternalIssuer)}
	}}
}

// Provision implements caddy.Provisioner.
func (iss *caddyLocalIssuer) Provision(ctx caddy.Context) error {
	e, err := lookup(iss.Instance)
	if err != nil {
		return err
	}
	iss.edge = e
	if iss.Kind != issueAny && iss.Kind != issuePrivate {
		return fmt.Errorf("issuer kind %q", iss.Kind)
	}
	return iss.InternalIssuer.Provision(ctx)
}

// SetConfig records the certificate configuration, as the ACME issuers
// do, for the development edge whose only issuer this is.
func (iss *caddyLocalIssuer) SetConfig(cfg *certmagic.Config) { iss.edge.setTLS(nil, cfg) }

// PreCheck implements certmagic.PreChecker.
func (iss *caddyLocalIssuer) PreCheck(ctx context.Context, names []string, interactive bool) error {
	for _, n := range names {
		if !iss.edge.takes(iss.Kind, n) {
			return fmt.Errorf("the local CA doesn't certify the public name %s", n)
		}
	}
	return nil
}

// caddyDNS publishes DNS-01 challenge records through the panel.
type caddyDNS struct {
	Instance string `json:"instance"`
	edge     *Edge
}

// CaddyModule implements caddy.Module.
func (caddyDNS) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "dns.providers.racksail", New: func() caddy.Module { return new(caddyDNS) }}
}

// Provision implements caddy.Provisioner.
func (d *caddyDNS) Provision(ctx caddy.Context) error {
	e, err := lookup(d.Instance)
	d.edge = e
	return err
}

func (d *caddyDNS) each(ctx context.Context, kind, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	var done []libdns.Record
	for _, rec := range recs {
		rr := rec.RR()
		q := Question{Kind: kind, Zone: strings.TrimSuffix(zone, "."), Record: &DNSRecord{Name: rr.Name, Type: rr.Type, Value: rr.Data, TTL: rr.TTL}}
		if err := d.edge.ask(ctx, q); err != nil {
			return done, err
		}
		done = append(done, rec)
	}
	return done, nil
}

// AppendRecords implements libdns.RecordAppender.
func (d *caddyDNS) AppendRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	return d.each(ctx, AskPresentDNS, zone, recs)
}

// DeleteRecords implements libdns.RecordDeleter.
func (d *caddyDNS) DeleteRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	return d.each(ctx, AskCleanUpDNS, zone, recs)
}

// caddyEvents follows certificate events, for the certificate list.
type caddyEvents struct {
	Instance string `json:"instance"`
	edge     *Edge
}

// CaddyModule implements caddy.Module.
func (caddyEvents) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "events.handlers.racksail", New: func() caddy.Module { return new(caddyEvents) }}
}

// Provision implements caddy.Provisioner.
func (h *caddyEvents) Provision(ctx caddy.Context) error {
	e, err := lookup(h.Instance)
	h.edge = e
	return err
}

// Handle implements caddyevents.Handler.
func (h *caddyEvents) Handle(ctx context.Context, ev caddy.Event) error {
	name, _ := ev.Data["identifier"].(string)
	if name == "" {
		return nil
	}
	switch ev.Name() {
	case "cert_obtaining":
		h.edge.keeper.note(name, CertObtaining, "")
	case "cert_obtained":
		h.edge.keeper.note(name, CertValid, "")
	case "cert_failed":
		msg := "obtaining the certificate failed"
		if err, ok := ev.Data["error"].(error); ok {
			msg = err.Error()
		}
		h.edge.keeper.note(name, CertFailed, msg)
	}
	return nil
}

// Certificate states.
const (
	CertObtaining = "obtaining"
	CertValid     = "valid"
	CertFailed    = "failed"
	CertMissing   = "missing"
	CertExpired   = "expired"
)

// subjectState is what the edge knows of a subject's certificate.
type subjectState struct {
	status  string
	err     string
	changed time.Time
}

// certKeeper obtains the certificates of auto routes before anyone visits
// and renews them whether or not anyone does, which on-demand certificates
// otherwise only get during handshakes.
type certKeeper struct {
	e *Edge

	mu     sync.Mutex
	magic  *certmagic.Config
	kept   map[string]*keptSubject
	states map[string]subjectState
	now    func() time.Time
}

// keptSubject is a subject the keeper works on; its context ends when the
// subject is no longer served.
type keptSubject struct {
	ctx    context.Context
	cancel context.CancelFunc
	busy   bool
}

func newKeeper(e *Edge) *certKeeper {
	return &certKeeper{e: e, kept: map[string]*keptSubject{}, states: map[string]subjectState{}, now: time.Now}
}

// renewEvery is how often kept certificates are checked for renewal.
const renewEvery = 6 * time.Hour

func (k *certKeeper) note(name, status, msg string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.states[name] = subjectState{status: status, err: msg, changed: k.now()}
}

// sync starts obtaining certificates for subjects that are new, stops
// working on those no longer served, and tells whether any went away. A
// new certificate configuration, after Caddy started or reloaded, starts
// over with it.
func (k *certKeeper) sync() (stale bool) {
	routes, _ := k.e.table.Routes(context.Background())
	want := keptSubjects(routes)
	tlsApp, magic := k.e.tls()
	k.mu.Lock()
	defer k.mu.Unlock()
	if magic != k.magic {
		for s, ks := range k.kept {
			ks.cancel()
			delete(k.kept, s)
		}
		k.magic = magic
	}
	for s, ks := range k.kept {
		if !slices.Contains(want, s) {
			ks.cancel()
			delete(k.kept, s)
			delete(k.states, s)
			stale = true
		}
	}
	if magic == nil {
		// Caddy isn't running; subjects are picked up when it is.
		return stale
	}
	names := map[string]struct{}{}
	for _, s := range want {
		if _, ok := k.kept[s]; ok {
			continue
		}
		names[s] = struct{}{}
		ctx, cancel := context.WithCancel(context.Background())
		k.kept[s] = &keptSubject{ctx: ctx, cancel: cancel}
		if _, ok := k.states[s]; !ok {
			k.states[s] = subjectState{status: CertObtaining, changed: k.now()}
		}
		go k.obtain(ctx, magic, s)
	}
	if tlsApp != nil && len(names) > 0 {
		// Managed names are dropped from Caddy's cache when a reload no
		// longer manages them.
		if err := tlsApp.Manage(names); err != nil {
			k.e.log.Warn("registering certificate names", "error", err)
		}
	}
	return stale
}

// obtain gets a subject's certificate unless storage has it, retrying in
// the background, and loads it for handshakes.
func (k *certKeeper) obtain(ctx context.Context, magic *certmagic.Config, subject string) {
	if err := magic.ObtainCertAsync(ctx, subject); err != nil {
		if ctx.Err() == nil {
			k.note(subject, CertFailed, err.Error())
		}
		return
	}
	k.load(ctx, magic, subject)
}

func (k *certKeeper) load(ctx context.Context, magic *certmagic.Config, subject string) {
	if _, err := magic.CacheManagedCertificate(ctx, subject); err != nil {
		if ctx.Err() == nil && !errors.Is(err, fs.ErrNotExist) {
			k.e.log.Warn("loading a certificate", "subject", subject, "error", err)
		}
		return
	}
	k.note(subject, CertValid, "")
}

// run renews kept certificates when they're due until ctx is done.
func (k *certKeeper) run(ctx context.Context) {
	t := time.NewTicker(renewEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			k.mu.Lock()
			for s, ks := range k.kept {
				ks.cancel()
				delete(k.kept, s)
			}
			k.mu.Unlock()
			return
		case <-t.C:
			k.renewAll(false)
		}
	}
}

// renewAll renews the kept certificates that are due, or all of them.
func (k *certKeeper) renewAll(force bool) {
	k.mu.Lock()
	magic := k.magic
	subjects := make([]string, 0, len(k.kept))
	for s := range k.kept {
		subjects = append(subjects, s)
	}
	k.mu.Unlock()
	if magic == nil {
		return
	}
	for _, s := range subjects {
		if force || renewalDue(context.Background(), magic, s, k.now()) {
			k.renew(s, force)
		}
	}
}

// renewalDue reports whether a kept certificate has less than a third of
// its lifetime left, as certmagic judges it, or isn't kept at all.
func renewalDue(ctx context.Context, magic *certmagic.Config, subject string, now time.Time) bool {
	certPEM, _, _, err := storedCertificate(ctx, magic, subject)
	if err != nil {
		return true
	}
	leaf, err := leafOf(certPEM)
	if err != nil {
		return true
	}
	return leaf.NotAfter.Sub(now) < leaf.NotAfter.Sub(leaf.NotBefore)/3
}

// renew renews a kept certificate in the background, one renewal at a
// time per subject; certmagic retries until it succeeds or the subject is
// no longer served. It reports whether the subject is kept.
func (k *certKeeper) renew(subject string, force bool) bool {
	k.mu.Lock()
	ks, kept := k.kept[subject]
	magic := k.magic
	if !kept || magic == nil {
		k.mu.Unlock()
		return false
	}
	if ks.busy {
		k.mu.Unlock()
		return true
	}
	ks.busy = true
	k.mu.Unlock()
	go func() {
		defer func() {
			k.mu.Lock()
			ks.busy = false
			k.mu.Unlock()
		}()
		if err := magic.RenewCertAsync(ks.ctx, subject, force); err != nil {
			if ks.ctx.Err() == nil {
				k.note(subject, CertFailed, err.Error())
			}
			return
		}
		k.load(ks.ctx, magic, subject)
	}()
	return true
}

// Certificate describes a certificate the edge serves.
type Certificate struct {
	// Subject is the name the certificate is kept under.
	Subject string   `json:"subject"`
	Names   []string `json:"names,omitempty" doc:"Names the certificate is valid for"`
	// Source is acme, local or uploaded.
	Source    string    `json:"source" enum:"acme,local,uploaded"`
	Issuer    string    `json:"issuer,omitempty" doc:"Common name of the issuing CA"`
	NotBefore time.Time `json:"notBefore,omitzero"`
	NotAfter  time.Time `json:"notAfter,omitzero"`
	// Status is obtaining, valid, failed, missing or expired.
	Status string `json:"status" enum:"obtaining,valid,failed,missing,expired"`
	Error  string `json:"error,omitempty" doc:"Why the last attempt failed"`
	// Owners are the resources whose routes use it.
	Owners []store.ID `json:"owners,omitempty"`
}

// storedCertificate loads the certificate and key kept for a subject, and
// the issuer that issued them.
func storedCertificate(ctx context.Context, magic *certmagic.Config, subject string) (certPEM, keyPEM []byte, issuer certmagic.Issuer, err error) {
	for _, iss := range magic.Issuers {
		key := iss.IssuerKey()
		certPEM, err = magic.Storage.Load(ctx, certmagic.StorageKeys.SiteCert(key, subject))
		if err != nil {
			continue
		}
		keyPEM, err = magic.Storage.Load(ctx, certmagic.StorageKeys.SitePrivateKey(key, subject))
		if err != nil {
			continue
		}
		return certPEM, keyPEM, iss, nil
	}
	return nil, nil, nil, fmt.Errorf("no certificate is kept for %s: %w", subject, fs.ErrNotExist)
}

func leafOf(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("no PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// Certificates lists the certificates of the names the routes serve: those
// obtained for auto routes, those obtained on demand, and uploaded ones.
func (e *Edge) Certificates(ctx context.Context) []Certificate {
	routes, _ := e.table.Routes(ctx)
	_, magic := e.tls()
	owners := map[string][]store.ID{}
	uploaded := map[string][]store.ID{}
	for _, r := range routes {
		for _, h := range r.Hosts {
			switch r.tlsMode() {
			case TLSAuto:
				if h != CatchAll {
					owners[h] = appendOwner(owners[h], r.Owner)
				}
			case TLSCertificate:
				uploaded[r.TLS.Certificate] = appendOwner(uploaded[r.TLS.Certificate], r.Owner)
			}
		}
	}
	e.keeper.mu.Lock()
	states := make(map[string]subjectState, len(e.keeper.states))
	for s, st := range e.keeper.states {
		states[s] = st
		if _, ok := owners[s]; !ok {
			// Obtained on demand.
			if r, ok := e.table.Match(s, "/"); ok {
				owners[s] = appendOwner(owners[s], r.Owner)
			}
		}
	}
	e.keeper.mu.Unlock()

	now := time.Now()
	var out []Certificate
	for subject, ids := range owners {
		c := Certificate{Subject: subject, Source: "acme", Status: CertMissing, Owners: ids}
		if st, ok := states[subject]; ok {
			c.Status, c.Error = st.status, st.err
		}
		if magic != nil {
			if certPEM, _, issuer, err := storedCertificate(ctx, magic, subject); err == nil {
				if leaf, err := leafOf(certPEM); err == nil {
					c.Names, c.Issuer, c.NotBefore, c.NotAfter = leaf.DNSNames, leaf.Issuer.CommonName, leaf.NotBefore, leaf.NotAfter
					for _, ip := range leaf.IPAddresses {
						c.Names = append(c.Names, ip.String())
					}
					if c.Status != CertFailed || c.Error == "" {
						c.Status = CertValid
					}
					if now.After(leaf.NotAfter) {
						c.Status = CertExpired
					}
				}
				if _, local := issuer.(*caddyLocalIssuer); local {
					c.Source = "local"
				}
			}
		}
		out = append(out, c)
	}
	for name, ids := range uploaded {
		c := Certificate{Subject: name, Source: "uploaded", Status: CertMissing, Owners: ids}
		if cert, err := e.certs.get(name); err != nil {
			c.Error = err.Error()
		} else if cert.Leaf != nil {
			leaf := cert.Leaf
			c.Names, c.Issuer, c.NotBefore, c.NotAfter, c.Status = leaf.DNSNames, leaf.Issuer.CommonName, leaf.NotBefore, leaf.NotAfter, CertValid
			if now.After(leaf.NotAfter) {
				c.Status = CertExpired
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out
}

func appendOwner(ids []store.ID, id store.ID) []store.ID {
	if slices.Contains(ids, id) {
		return ids
	}
	return append(ids, id)
}

// ExportCertificate returns an issued certificate's chain and key in PEM,
// for services other than the edge that serve the same names, such as
// mail.
func (e *Edge) ExportCertificate(ctx context.Context, subject string) (certPEM, keyPEM []byte, err error) {
	_, magic := e.tls()
	if magic == nil {
		return nil, nil, errors.New("the edge isn't serving")
	}
	certPEM, keyPEM, _, err = storedCertificate(ctx, magic, subject)
	return certPEM, keyPEM, err
}

// RenewCertificate starts renewing a kept certificate now; its status in
// Certificates follows the renewal.
func (e *Edge) RenewCertificate(subject string) error {
	if !e.Running() {
		return errors.New("the edge isn't serving")
	}
	if !e.keeper.renew(subject, true) {
		return fmt.Errorf("no auto route serves %s: %w", subject, fs.ErrNotExist)
	}
	return nil
}
