package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// openStream opens a live stream as an actor and waits until it is ready.
func (s *stack) openStream(actor, path string) (*http.Response, *bufio.Reader) {
	s.t.Helper()
	req, _ := http.NewRequest("GET", s.http.URL+path, nil)
	req.Header.Set("X-Test-Actor", actor)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { resp.Body.Close() })
	r := bufio.NewReader(resp.Body)
	if line, err := r.ReadString('\n'); err != nil || !strings.HasPrefix(line, ": connected") {
		s.t.Fatalf("stream = %q, %v", line, err)
	}
	return resp, r
}

// Closing the server ends live streams and the waits for actions at once,
// so shutting down isn't held up by clients that would stay connected.
func TestClosingEndsStreamsAndWaits(t *testing.T) {
	s := newStack(t, RateLimit{})
	hold := make(chan struct{})
	defer close(hold)
	if err := s.jobs.Handle(jobs.Type{Name: "test.hold", Run: func(ctx context.Context, job *jobs.Job, args json.RawMessage, r jobs.Reporter) (any, error) {
		select {
		case <-hold:
		case <-ctx.Done():
		}
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	site := s.createSite("owner", "close.test")

	_, events := s.openStream("admin", "/api/v1/events/stream")
	// Every worker is busy, so a short action's job waits, and so does its
	// request.
	for range 4 {
		j, err := s.jobs.Enqueue(context.Background(), "test.hold", nil, jobs.Options{})
		if err != nil {
			t.Fatal(err)
		}
		s.waitRunning(j.ID)
	}
	answered := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest("POST", s.http.URL+"/api/v1/test/sites/"+string(site)+"/actions/ping", nil)
		req.Header.Set("X-Test-Actor", "owner")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			answered <- 0
			return
		}
		resp.Body.Close()
		answered <- resp.StatusCode
	}()
	select {
	case code := <-answered:
		t.Fatalf("the action answered %d before its job ran", code)
	case <-time.After(100 * time.Millisecond):
	}

	s.server.Close()
	select {
	case code := <-answered:
		if code != http.StatusAccepted {
			t.Errorf("the waiting action answered %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing didn't end the wait")
	}
	ended := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(events)
		ended <- err
	}()
	select {
	case err := <-ended:
		if err != nil {
			t.Errorf("the stream ended badly: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing didn't end the stream")
	}
	// A stream opened afterwards ends as soon as it starts.
	req, _ := http.NewRequest("GET", s.http.URL+"/api/v1/events/stream", nil)
	req.Header.Set("X-Test-Actor", "admin")
	late, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer late.Body.Close()
	if rest, err := io.ReadAll(late.Body); err != nil || strings.Contains(string(rest), "event:") {
		t.Errorf("a stream after closing = %q, %v", rest, err)
	}
}

// createSite creates a site in the stack's account and waits for it.
func (s *stack) createSite(actor, domain string) (id store.ID) {
	s.t.Helper()
	var cs changeset.ChangeSet
	resp := s.call(actor, "POST", "/api/v1/test/sites", map[string]any{
		"scope": map[string]any{"level": "account", "id": s.account},
		"spec":  map[string]any{"domains": []string{domain}, "content": domain},
	}, &cs)
	if resp.StatusCode != http.StatusAccepted {
		s.t.Fatalf("creating %s = %d %s", domain, resp.StatusCode, bodyOf(resp))
	}
	if final := s.finish(actor, &cs); final.State != changeset.StateApplied {
		s.t.Fatalf("creating %s = %+v", domain, final)
	}
	return cs.Changes[0].ID
}

// waitRunning waits until a job runs.
func (s *stack) waitRunning(id store.ID) {
	s.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if j, err := s.jobs.Get(context.Background(), id); err == nil && j.State == jobs.StateRunning {
			return
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("job %s never started", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
