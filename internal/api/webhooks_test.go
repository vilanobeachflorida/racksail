package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/events/webhook"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// createWebhook creates a webhook as the admin and waits for it.
func (s *stack) createWebhook(body map[string]any) *changeset.ChangeSet {
	s.t.Helper()
	var cs changeset.ChangeSet
	if resp := s.call("admin", "POST", "/api/v1/core/webhooks", body, &cs); resp.StatusCode != http.StatusAccepted {
		s.t.Fatalf("create = %d %s", resp.StatusCode, bodyOf(resp))
	}
	return s.finish("admin", &cs)
}

// Canceling a webhook test while the receiver takes its time: the job ends
// canceled, the change set failed and the delivery failed, and nothing
// claims the test went through.
func TestCancelingAWebhookTest(t *testing.T) {
	s := newStack(t, RateLimit{})
	hold := make(chan struct{})
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Racksail-Event"), "ping") {
			select {
			case <-hold:
			case <-r.Context().Done():
			}
		}
	}))
	defer receiver.Close()
	defer close(hold)
	id := s.createWebhook(map[string]any{
		"spec": map[string]any{"url": receiver.URL, "events": []string{"core.nothing.*"}}, "secrets": map[string]string{"secret": "k"},
	}).Changes[0].ID
	sub := s.log.Subscribe(events.Filter{Types: []string{"core.webhook.*", "core.changeset.*"}}, 16)
	defer sub.Close()

	// The test is waited for, so it is asked for in the background.
	answered := make(chan *changeset.ChangeSet, 1)
	go func() {
		req, _ := http.NewRequest("POST", s.http.URL+"/api/v1/core/webhooks/"+string(id)+"/actions/test", nil)
		req.Header.Set("X-Test-Actor", "admin")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			answered <- nil
			return
		}
		defer resp.Body.Close()
		var cs changeset.ChangeSet
		json.NewDecoder(resp.Body).Decode(&cs)
		answered <- &cs
	}()
	var job *jobs.Job
	deadline := time.Now().Add(3 * time.Second)
	for job == nil {
		var list struct {
			Jobs []*jobs.Job `json:"jobs"`
		}
		s.call("admin", "GET", "/api/v1/jobs?type=changeset.apply&state=running", nil, &list)
		if len(list.Jobs) == 1 {
			job = list.Jobs[0]
		} else if time.Now().After(deadline) {
			t.Fatal("the test's job never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if resp := s.call("admin", "POST", "/api/v1/jobs/"+string(job.ID)+"/cancel", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var cs *changeset.ChangeSet
	select {
	case cs = <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("the test never answered")
	}
	if cs == nil || cs.State != changeset.StateFailed || cs.Error != "The operation was canceled before it finished." {
		t.Fatalf("the test's change set = %+v", cs)
	}
	var final jobs.Job
	if s.call("admin", "GET", "/api/v1/jobs/"+string(job.ID), nil, &final); final.State != jobs.StateCanceled {
		t.Errorf("job = %+v", final)
	}
	var list struct {
		Deliveries []webhook.Delivery `json:"deliveries"`
	}
	s.call("admin", "GET", "/api/v1/core/webhooks/"+string(id)+"/deliveries", nil, &list)
	if len(list.Deliveries) != 1 || list.Deliveries[0].State != webhook.DeliveryFailed || !list.Deliveries[0].Test {
		t.Errorf("deliveries = %+v", list.Deliveries)
	}
	var seen []string
	for {
		select {
		case e := <-sub.C:
			seen = append(seen, e.Type)
			continue
		case <-time.After(200 * time.Millisecond):
		}
		break
	}
	if strings.Join(seen, " ") != "core.webhook.failed core.changeset.failed" {
		t.Errorf("events = %v", seen)
	}
}

// A deleted webhook's ID isn't given to a new one, which would see its
// history and could sign with its secret.
func TestDeletedWebhookIDsAreNotReused(t *testing.T) {
	s := newStack(t, RateLimit{})
	spec := map[string]any{"url": "https://hooks.example.com/a", "events": []string{"*"}}
	created := s.createWebhook(map[string]any{"spec": spec, "secrets": map[string]string{"secret": "first secret"}})
	id := created.Changes[0].ID
	var hook struct {
		Spec webhook.WebhookSpec `json:"spec"`
	}
	s.call("admin", "GET", "/api/v1/core/webhooks/"+string(id), nil, &hook)
	ref := hook.Spec.Secret
	var del changeset.ChangeSet
	if resp := s.call("admin", "DELETE", "/api/v1/core/webhooks/"+string(id), nil, &del); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("delete = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("admin", &del)
	if resp := s.call("admin", "GET", "/api/v1/core/webhooks/"+string(id)+"/history", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the deleted webhook = %d", resp.StatusCode)
	}
	for _, path := range []string{"/api/v1/core/webhooks?dryRun=true", "/api/v1/core/webhooks"} {
		resp := s.call("admin", "POST", path, map[string]any{"id": id, "spec": spec, "secrets": map[string]string{"secret": "second secret"}}, nil)
		if resp.StatusCode != http.StatusConflict || s.problem(resp) != store.ErrIDUsed.Code {
			t.Errorf("%s with the deleted webhook's ID = %d %s", path, resp.StatusCode, bodyOf(resp))
		}
	}
	// Nor can another webhook point at its secret.
	withRef := map[string]any{"url": "https://hooks.example.com/b", "events": []string{"*"}, "secret": ref}
	if resp := s.call("admin", "POST", "/api/v1/core/webhooks?dryRun=true", map[string]any{"spec": withRef}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("another webhook with its secret = %d %s", resp.StatusCode, bodyOf(resp))
	}
	// Undoing the delete brings it back, as it was.
	var undo changeset.ChangeSet
	if resp := s.call("admin", "POST", "/api/v1/changesets/"+string(del.ID)+"/undo", nil, &undo); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("undo = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("admin", &undo)
	var history historyBody
	if resp := s.call("admin", "GET", "/api/v1/core/webhooks/"+string(id)+"/history", nil, &history); resp.StatusCode != http.StatusOK || len(history.Versions) != 3 {
		t.Errorf("history after the undo = %d %+v", resp.StatusCode, history)
	}
	if body := bodyOf(s.call("admin", "GET", "/api/v1/core/webhooks/"+string(id), nil, nil)); !strings.Contains(body, ref) {
		t.Errorf("it comes back with its own secret: %s", body)
	}
}

// A webhook can't point at the server itself or its private network unless
// racksail.toml allows it.
func TestWebhooksStayOffPrivateAddresses(t *testing.T) {
	s := newStack(t, RateLimit{})
	for _, url := range []string{"http://10.1.2.3/hook", "http://169.254.169.254/latest/meta-data", "http://[fd00::1]/hook"} {
		resp := s.call("admin", "POST", "/api/v1/core/webhooks?dryRun=true", map[string]any{
			"spec": map[string]any{"url": url, "events": []string{"*"}}, "secrets": map[string]string{"secret": "k"},
		}, nil)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(bodyOf(resp), "spec.url") {
			t.Errorf("%s = %d %s", url, resp.StatusCode, bodyOf(resp))
		}
	}
}
