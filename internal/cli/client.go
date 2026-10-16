package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/updates"
)

// Environment variables the client commands read. The token is only ever
// read from the environment or a profile, so it stays out of shell
// history.
const (
	ServerEnv  = "RACKSAIL_SERVER"
	TokenEnv   = "RACKSAIL_TOKEN"
	ProfileEnv = "RACKSAIL_PROFILE"
)

// DefaultServer is where a server's panel listens unless racksail.toml
// says otherwise: HTTPS on port 8443, which the server reaches itself at
// on loopback.
const DefaultServer = "https://localhost:8443"

// localPanel is the panel of the server the command runs on: where it
// listens, as racksail.toml says when this is a Racksail server, and the
// certificate it made for itself, which the command trusts for it when it
// may read it.
func localPanel() (server, caFile string) {
	cfg, err := config.Load(system.ConfigFile)
	if err != nil {
		cfg = config.Default()
	}
	host, port, err := net.SplitHostPort(cfg.Panel.Listen)
	if err != nil {
		return DefaultServer, ""
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "localhost"
	}
	server = "https://" + net.JoinHostPort(host, port)
	if cfg.Panel.TLSCert == "" {
		cert := filepath.Join(cfg.Paths.State, "tls", "panel.crt")
		if f, err := os.Open(cert); err == nil {
			f.Close()
			caFile = cert
		}
	}
	return server, caFile
}

// Options are the settings commands that call a panel's API share.
type Options struct {
	Profile string
	Server  string
	CAFile  string
	Output  string
	DryRun  bool
	// Detach returns once a job is queued instead of following it.
	Detach bool
}

// MutatingAnnotation marks commands that change something. Only they get
// --dry-run.
const MutatingAnnotation = "racksail:mutating"

// Mutating marks a command as one that changes something.
func Mutating(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[MutatingAnnotation] = "true"
	return cmd
}

// IsMutating reports whether Mutating marked the command.
func IsMutating(cmd *cobra.Command) bool { return cmd.Annotations[MutatingAnnotation] == "true" }

// Output formats.
var outputs = []string{"table", "json", "yaml"}

// AddFlags adds the flags every client command has, and --dry-run to those
// that change something.
func AddFlags(cmd *cobra.Command, o *Options) {
	f := cmd.Flags()
	f.StringVar(&o.Profile, "profile", "", "profile to use instead of the current one; "+ProfileEnv+" sets the default")
	f.StringVar(&o.Server, "server", "", "panel address, instead of the profile's; "+ServerEnv+" sets the default, and without either the panel on this server, "+DefaultServer+" unless racksail.toml says otherwise")
	f.StringVar(&o.CAFile, "ca-file", "", "certificate authority to trust for the panel, such as its self-signed certificate")
	f.StringVarP(&o.Output, "output", "o", "table", "output format: table, json or yaml")
	if IsMutating(cmd) {
		f.BoolVar(&o.DryRun, "dry-run", false, "show the change set without applying it")
	}
	cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions(outputs, cobra.ShellCompDirectiveNoFileComp))
	cmd.RegisterFlagCompletionFunc("profile", completeProfiles)
}

// checkOutput refuses an output format the commands don't print.
func (o *Options) checkOutput() error {
	for _, f := range outputs {
		if o.Output == f {
			return nil
		}
	}
	return fmt.Errorf("--output is table, json or yaml, not %q", o.Output)
}

// Connect works out which panel to call, and with which token: --server or
// RACKSAIL_SERVER, or else the profile's; RACKSAIL_TOKEN, or else the
// profile's token, which is only ever sent to the profile's own server.
func (o *Options) Connect() (*Client, error) {
	if err := o.checkOutput(); err != nil {
		return nil, err
	}
	server := o.Server
	if server == "" {
		server = os.Getenv(ServerEnv)
	}
	token := os.Getenv(TokenEnv)
	var prof *Profile
	name := o.Profile
	if name == "" {
		name = os.Getenv(ProfileEnv)
	}
	if server == "" || token == "" || name != "" {
		profiles, err := LoadProfiles()
		if err != nil {
			return nil, err
		}
		var ok bool
		name, prof, ok = profiles.Get(name)
		if !ok && name != "" {
			return nil, fmt.Errorf("there's no profile called %s; racksail profile list shows them", name)
		}
	}
	if server == "" && prof != nil {
		server = prof.Server
	}
	caFile := o.CAFile
	if server == "" {
		var local string
		server, local = localPanel()
		if caFile == "" {
			caFile = local
		}
	}
	server, err := NormalizeServer(server)
	if err != nil {
		return nil, err
	}
	if prof != nil && sameServer(prof.Server, server) {
		if token == "" {
			token = prof.Token
		}
		if caFile == "" {
			caFile = prof.CAFile
		}
	} else {
		name = ""
	}
	return NewClient(server, token, caFile, name)
}

func sameServer(a, b string) bool {
	na, errA := NormalizeServer(a)
	nb, errB := NormalizeServer(b)
	return errA == nil && errB == nil && na == nb
}

// Client calls a panel's API.
type Client struct {
	// Server is the panel's address, and Profile the profile it came from;
	// empty when none did.
	Server  string
	Profile string
	token   string
	http    *http.Client
}

// UserAgent is what the command says it is. The panel records requests
// with it as coming from the CLI.
var UserAgent = "racksail/" + updates.Build().Version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"

// NewClient makes a client for a panel, trusting caFile's certificates on
// top of the system's when it's given.
func NewClient(server, token, caFile, profile string) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = 2 * time.Minute
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no PEM certificate", caFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &Client{Server: strings.TrimSuffix(server, "/"), Profile: profile, token: token, http: &http.Client{Transport: transport}}, nil
}

// HasToken reports whether the client signs in.
func (c *Client) HasToken() bool { return c.token != "" }

// Request is one call of the API.
type Request struct {
	Method string
	// Path has its parameters filled in, and starts with /api/v1.
	Path   string
	Query  url.Values
	Header http.Header
	// Body is sent as JSON when it isn't nil.
	Body any
	// Accept is the media type asked for; JSON when empty.
	Accept string
}

// Do sends a request. A problem the panel answers with comes back as a
// *Problem; otherwise the caller reads and closes the response.
func (c *Client) Do(ctx context.Context, r Request) (*http.Response, error) {
	u := c.Server + r.Path
	if len(r.Query) > 0 {
		u += "?" + r.Query.Encode()
	}
	var body io.Reader
	if r.Body != nil {
		raw, err := json.Marshal(r.Body)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u, body)
	if err != nil {
		return nil, err
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	accept := r.Accept
	if accept == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept+", application/problem+json")
	req.Header.Set("User-Agent", UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("can't reach %s: %w", c.Server, err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, readProblem(resp)
	}
	return resp, nil
}

// Get calls GET path and decodes the JSON answer into out.
func (c *Client) Get(ctx context.Context, path string, out any) (int, error) {
	resp, err := c.Do(ctx, Request{Method: http.MethodGet, Path: path})
	if err != nil {
		var p *Problem
		if errors.As(err, &p) {
			return p.Status, err
		}
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswer)).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("unexpected answer from %s: %w", c.Server, err)
		}
	}
	return resp.StatusCode, nil
}

// maxAnswer bounds the JSON answers read whole.
const maxAnswer = 64 << 20

// Problem is an error the panel answered with, as RFC 9457 describes.
type Problem struct {
	Status int            `json:"status"`
	Code   string         `json:"code"`
	Detail string         `json:"detail"`
	Title  string         `json:"title"`
	Fields []FieldProblem `json:"errors"`
	// flags names the flags that set fields of the request body, so the
	// message can say which flag a problem is with.
	flags map[string]string
}

// FieldProblem is what's wrong with one field of a request.
type FieldProblem struct {
	Location string `json:"location"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

func readProblem(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	p := &Problem{Status: resp.StatusCode}
	if err := json.Unmarshal(raw, p); err != nil || (p.Detail == "" && p.Code == "") {
		p.Detail = strings.TrimSpace(string(raw))
		if len(p.Detail) > 300 || p.Detail == "" {
			p.Detail = http.StatusText(resp.StatusCode)
		}
	}
	p.Status = resp.StatusCode
	return p
}

func (p *Problem) Error() string {
	var b strings.Builder
	b.WriteString(p.Detail)
	if p.Detail == "" {
		b.WriteString(p.Title)
	}
	if p.Code != "" {
		fmt.Fprintf(&b, " (%s)", p.Code)
	}
	for _, f := range p.Fields {
		fmt.Fprintf(&b, "\n  %s: %s", p.where(f.Location), f.Message)
	}
	switch {
	case p.Status == http.StatusUnauthorized:
		b.WriteString("\nSign in with racksail login, or set " + TokenEnv + " to an API token.")
	case p.Code == "auth.step_up_required":
		b.WriteString("\nConfirm it's you in the panel, then try again.")
	}
	return b.String()
}

// where says which flag set a field, or the field's location when no
// flag did.
func (p *Problem) where(location string) string {
	field := strings.TrimPrefix(location, "body.")
	if flag, ok := p.flags[field]; ok {
		return "--" + flag
	}
	if field == "" {
		return "request"
	}
	return field
}
