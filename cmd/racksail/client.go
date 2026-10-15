package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/updates"
)

// ClientOptions are the settings shared by commands that call a server's
// API. The token only comes from the environment, so it never ends up in
// shell history.
type ClientOptions struct {
	Server string
	CAFile string
	Output string
	DryRun bool
}

// clientCommands builds the commands that call the API. Commands generated
// from the OpenAPI document append their builders here from init functions,
// next to the hand-written ones; each gets the shared client flags.
var clientCommands = []func(opts *ClientOptions) *cobra.Command{newStatusCommand}

// mutatingAnnotation marks client commands that change something. Only they
// get --dry-run; generated commands set it for every operation that isn't a
// GET.
const mutatingAnnotation = "racksail:mutating"

// mutating marks a client command as one that changes something.
func mutating(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[mutatingAnnotation] = "true"
	return cmd
}

func addClientFlags(cmd *cobra.Command, opts *ClientOptions) {
	server := os.Getenv("RACKSAIL_SERVER")
	if server == "" {
		server = "http://127.0.0.1:7070"
	}
	f := cmd.Flags()
	f.StringVar(&opts.Server, "server", server, "panel address; RACKSAIL_SERVER sets the default")
	f.StringVar(&opts.CAFile, "ca-file", "", "certificate authority to trust for the panel, such as its self-signed certificate")
	f.StringVarP(&opts.Output, "output", "o", "table", "output format: table or json")
	if cmd.Annotations[mutatingAnnotation] == "true" {
		f.BoolVar(&opts.DryRun, "dry-run", false, "show the change set without applying it")
	}
}

// Client calls a server's API.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient builds a client from the options and RACKSAIL_TOKEN.
func (o *ClientOptions) NewClient() (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no PEM certificate", o.CAFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &Client{
		base:  strings.TrimSuffix(o.Server, "/"),
		token: os.Getenv("RACKSAIL_TOKEN"),
		http:  &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}, nil
}

// Get calls GET path and decodes the JSON answer into out. Problems become
// errors with the server's message.
func (c *Client) Get(ctx context.Context, path string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("can't reach %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/problem+json") {
		var p httpapi.Problem
		if err := json.Unmarshal(body, &p); err == nil && p.Detail != "" {
			return resp.StatusCode, fmt.Errorf("%s (%s)", p.Detail, p.Code)
		}
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("unexpected answer from %s: %w", c.base, err)
		}
	}
	return resp.StatusCode, nil
}

func newStatusCommand(opts *ClientOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether a server is healthy and which version it runs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := opts.NewClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var health api.HealthReport
			if _, err := c.Get(ctx, api.Prefix+"/health", &health); err != nil {
				return err
			}
			var version updates.BuildInfo
			if _, err := c.Get(ctx, api.Prefix+"/version", &version); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if opts.Output == "json" {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{"server": opts.Server, "health": health, "version": version})
			}
			fmt.Fprintf(out, "%s: racksail %s (%s), %s\n", opts.Server, version.Version, version.Channel, health.Status)
			for _, check := range health.Checks {
				line := fmt.Sprintf("  %-12s %s", check.Name, check.Status)
				if check.Message != "" {
					line += ": " + check.Message
				}
				fmt.Fprintln(out, line)
			}
			if health.Status != "ok" {
				return exitError{code: 2}
			}
			return nil
		},
	}
}
