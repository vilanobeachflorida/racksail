package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

type inboxAnswer struct {
	Items       []InboxItem    `json:"items"`
	Dismissed   int            `json:"dismissed"`
	Unavailable []InboxProblem `json:"unavailable"`
	Checking    []string       `json:"checking"`
}

func (s *stack) inboxOf(actor, query string) inboxAnswer {
	s.t.Helper()
	var out inboxAnswer
	if resp := s.call(actor, "GET", "/api/v1/inbox"+query, nil, &out); resp.StatusCode != http.StatusOK {
		s.t.Fatalf("inbox of %s = %d %s", actor, resp.StatusCode, bodyOf(resp))
	}
	return out
}

func itemIDs(items []InboxItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	slices.Sort(out)
	return out
}

func TestInbox(t *testing.T) {
	s := newStack(t, RateLimit{})
	s.machine.OfferUpgrade("sudo", "1.9.17-1")
	// A site whose page can't be written fails, and its change set with it.
	site := store.NewID("tsite")
	s.machine.FailNext("files.write", sitePath(site), errors.New("disk full"))
	var cs changeset.ChangeSet
	resp := s.call("owner", "POST", "/api/v1/test/sites", map[string]any{
		"id": site, "scope": map[string]any{"level": "account", "id": s.account},
		"spec": map[string]any{"domains": []string{"broken.test"}, "content": "x"},
	}, &cs)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if final := s.finish("owner", &cs); final.State != changeset.StateFailed {
		t.Fatalf("change set = %+v", final)
	}
	jobItem, siteItem := "job:"+string(cs.Job), "resource:"+string(site)

	owner := s.inboxOf("owner", "")
	if got, want := itemIDs(owner.Items), []string{jobItem, siteItem}; !slices.Equal(got, want) {
		t.Fatalf("the owner's inbox = %v, want %v", got, want)
	}
	for _, it := range owner.Items {
		switch it.ID {
		case jobItem:
			var types []string
			for _, a := range it.Actions {
				types = append(types, a.Type)
			}
			if it.Category != "job" || it.Code != "job_failed" || it.ChangeSet != cs.ID || !slices.Equal(types, []string{"retry_job", "undo_changeset", "open_job"}) {
				t.Errorf("job item = %+v", it)
			}
		case siteItem:
			if it.Category != "resource" || it.Severity != "error" || it.Subject != site || it.Kind != "test.site" || !strings.Contains(it.Title, "broken.test") {
				t.Errorf("site item = %+v", it)
			}
		}
	}
	// The admin also sees the server's updates; others see nothing of it.
	if got := itemIDs(s.inboxOf("admin", "").Items); !slices.Equal(got, []string{jobItem, siteItem, "updates:packages"}) {
		t.Errorf("the admin's inbox = %v", got)
	}
	for _, who := range []string{"stranger", "nobody"} {
		if got := s.inboxOf(who, ""); len(got.Items) != 0 {
			t.Errorf("%s's inbox = %v", who, itemIDs(got.Items))
		}
	}

	// Dismissing only changes what the owner sees.
	if resp := s.call("owner", "POST", "/api/v1/inbox/"+siteItem+"/dismiss", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("dismiss = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if got := s.inboxOf("owner", ""); !slices.Equal(itemIDs(got.Items), []string{jobItem}) || got.Dismissed != 1 {
		t.Errorf("after dismissing = %v, %d", itemIDs(got.Items), got.Dismissed)
	}
	if got := itemIDs(s.inboxOf("admin", "").Items); !slices.Contains(got, siteItem) {
		t.Errorf("the admin lost the item: %v", got)
	}
	withDismissed := s.inboxOf("owner", "?dismissed=true")
	for _, it := range withDismissed.Items {
		if (it.ID == siteItem) != (it.Dismissal != nil) {
			t.Errorf("dismissal of %s = %+v", it.ID, it.Dismissal)
		}
	}
	if resp := s.call("owner", "DELETE", "/api/v1/inbox/"+siteItem+"/dismiss", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("restore = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := s.call("owner", "DELETE", "/api/v1/inbox/"+siteItem+"/dismiss", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("restoring again = %d", resp.StatusCode)
	}
	if got := s.inboxOf("owner", ""); len(got.Items) != 2 || got.Dismissed != 0 {
		t.Errorf("after restoring = %v", itemIDs(got.Items))
	}
	// Items someone doesn't have can't be dismissed, and snoozes end within
	// a year.
	for actor, path := range map[string]string{"owner": "updates:packages", "stranger": siteItem} {
		resp := s.call(actor, "POST", "/api/v1/inbox/"+path+"/dismiss", nil, nil)
		if resp.StatusCode != http.StatusNotFound || s.problem(resp) != i18n.ErrNotFound.Code {
			t.Errorf("%s dismissing %s = %d %s", actor, path, resp.StatusCode, bodyOf(resp))
		}
	}
	for _, until := range []time.Time{time.Now().Add(-time.Minute), time.Now().Add(400 * 24 * time.Hour)} {
		resp := s.call("owner", "POST", "/api/v1/inbox/"+jobItem+"/dismiss", map[string]any{"until": until}, nil)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("snoozing until %s = %d %s", until, resp.StatusCode, bodyOf(resp))
		}
	}
	if resp := s.call("owner", "POST", "/api/v1/inbox/"+jobItem+"/dismiss", map[string]any{"until": time.Now().Add(time.Hour)}, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("snooze = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if got := s.inboxOf("owner", ""); !slices.Equal(itemIDs(got.Items), []string{siteItem}) {
		t.Errorf("after snoozing = %v", itemIDs(got.Items))
	}

	// Running the work again clears both.
	var retried struct {
		ID store.ID `json:"id"`
	}
	if resp := s.call("owner", "POST", "/api/v1/jobs/"+string(cs.Job)+"/retry", nil, &retried); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("retry = %d %s", resp.StatusCode, bodyOf(resp))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := s.jobs.Wait(ctx, retried.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.inboxOf("admin", ""); !slices.Equal(itemIDs(got.Items), []string{"updates:packages"}) {
		t.Errorf("after the retry = %v", itemIDs(got.Items))
	}
}

func TestInboxStream(t *testing.T) {
	s := newStack(t, RateLimit{})
	site := store.NewID("tsite")
	s.machine.FailNext("files.write", sitePath(site), errors.New("disk full"))
	req, err := http.NewRequestWithContext(t.Context(), "GET", s.http.URL+"/api/v1/inbox/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test-Actor", "owner")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	go func() {
		var cs changeset.ChangeSet
		s.call("owner", "POST", "/api/v1/test/sites", map[string]any{
			"id": site, "scope": map[string]any{"level": "account", "id": s.account},
			"spec": map[string]any{"domains": []string{"stream.test"}, "content": "x"},
		}, &cs)
	}()
	var reasons []string
	readEvents(t, resp, func(event, id, data string) bool {
		if event == "changed" {
			reasons = append(reasons, data)
		}
		return len(reasons) > 0
	})
	if len(reasons) == 0 || !strings.Contains(reasons[0], `"reason"`) {
		t.Errorf("changes = %v", reasons)
	}
}
