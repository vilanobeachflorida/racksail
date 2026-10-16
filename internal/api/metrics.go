package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// PermMetricsRead is the permission to read the panel's own metrics, which
// describe the whole server.
const PermMetricsRead auth.Permission = "core.metrics.read"

// Metric is one value the panel reports about itself.
type Metric struct {
	// Name is the metric's name in the Prometheus text format, such as
	// racksail_jobs_queued.
	Name string
	Help string
	// Counter says the value only grows while the panel runs; otherwise it
	// goes up and down.
	Counter bool
	Value   float64
}

var metricName = regexp.MustCompile(`^racksail_[a-z][a-z0-9_]*$`)

// apiMetrics are the API's own metrics.
func (s *Server) apiMetrics() []Metric {
	refused, callers := s.limiter.stats()
	return []Metric{
		{Name: "racksail_api_requests_total", Help: "Requests the panel received.", Counter: true, Value: float64(s.requests.Load())},
		{Name: "racksail_api_rate_limited_total", Help: "Requests refused because the caller was over its rate limit.", Counter: true, Value: float64(refused)},
		{Name: "racksail_api_rate_limit_callers", Help: "Callers the rate limiter is keeping track of.", Value: float64(callers)},
		{Name: "racksail_api_streams", Help: "Live streams open now.", Value: float64(s.streams.Load())},
	}
}

// renderMetrics writes metrics in the Prometheus text format, sorted by
// name.
func renderMetrics(metrics []Metric) ([]byte, error) {
	slices.SortStableFunc(metrics, func(a, b Metric) int { return strings.Compare(a.Name, b.Name) })
	var b bytes.Buffer
	for i, m := range metrics {
		if !metricName.MatchString(m.Name) {
			return nil, fmt.Errorf("api: %q isn't a metric name", m.Name)
		}
		if i > 0 && metrics[i-1].Name == m.Name {
			return nil, fmt.Errorf("api: the metric %s is reported twice", m.Name)
		}
		typ := "gauge"
		if m.Counter {
			typ = "counter"
		}
		help := strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(m.Help)
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s %s\n", m.Name, help, m.Name, typ, m.Name, strconv.FormatFloat(m.Value, 'g', -1, 64))
	}
	return b.Bytes(), nil
}

func (s *Server) registerMetrics() {
	op := huma.Operation{
		OperationID: "getMetrics", Method: http.MethodGet, Path: Prefix + "/metrics", Tags: []string{"System"}, Security: Protected,
		Summary: "Read the panel's metrics",
		Description: "Reports how the panel's own machinery is doing, in the Prometheus text format: jobs waiting and running, " +
			"the reconcile queue and its failures, webhook deliveries, and requests refused by the rate limits. " +
			"Monitoring tools can scrape it with an API token that has the core.metrics.read permission.",
		Responses: map[string]*huma.Response{"200": {
			Description: "The metrics, one per line with its help and type",
			Content:     map[string]*huma.MediaType{"text/plain": {Schema: &huma.Schema{Type: huma.TypeString}}},
		}},
	}
	s.withProblems(&op)
	Describe(&op, Info{Operation: "metrics", Permission: string(PermMetricsRead), Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, _ *struct{}) (*huma.StreamResponse, error) {
		if err := Authorize(ctx, s.authorizer(), PermMetricsRead, auth.Target{Scope: store.ServerScope()}); err != nil {
			return nil, err
		}
		metrics := s.apiMetrics()
		if s.opts.Metrics != nil {
			more, err := s.opts.Metrics(ctx)
			if err != nil {
				return nil, err
			}
			metrics = append(metrics, more...)
		}
		text, err := renderMetrics(metrics)
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hc huma.Context) {
			hc.SetHeader("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			hc.SetStatus(http.StatusOK)
			if _, err := hc.BodyWriter().Write(text); err != nil {
				s.opts.Log.Debug("writing the metrics", "error", err)
			}
		}}, nil
	})
}
