package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// SubmitFunc answers a retry with the first change set before it builds the
// request again, since by then the first request changed what building it
// reads. Only the same actor's retries are answered that way, and a dry run,
// which takes no key, is built and planned every time.
func TestSubmitFuncAnswersRetriesBeforeBuilding(t *testing.T) {
	s := newStack(t, RateLimit{})
	p := s.server.pipeline()
	errBuiltAgain := errors.New("built again")
	builds := 0
	build := func() (changeset.Request, error) {
		builds++
		if builds > 1 {
			// What the first request did makes building it again fail, as a
			// module that is on refuses to be turned on.
			return changeset.Request{}, errBuiltAgain
		}
		spec, _ := json.Marshal(siteSpec{Domains: []string{"once.test"}, Content: "x"})
		return changeset.Request{Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: "test.site", Scope: store.AccountScope(s.account), Spec: spec}}}, nil
	}
	owner := auth.WithActor(context.Background(), s.actors["owner"])
	params := ChangeParams{IdempotencyKey: "site-1"}
	first, err := SubmitFunc(owner, p, params, build)
	if err != nil || first.Status != http.StatusAccepted || first.Body.DryRun {
		t.Fatalf("first = %+v, %v", first, err)
	}
	s.finish("owner", first.Body)
	again, err := SubmitFunc(owner, p, params, build)
	if err != nil || again.Status != http.StatusAccepted || again.Body.ID != first.Body.ID || builds != 1 {
		t.Errorf("the retry = %+v, %v after %d builds, want %s without building again", again, err, builds, first.Body.ID)
	}
	if earlier, err := Replayed(owner, p, params); err != nil || earlier == nil || earlier.Body.ID != first.Body.ID {
		t.Errorf("replayed = %+v, %v", earlier, err)
	}
	for name, try := range map[string]func() (*ChangeSetOutput, error){
		"another actor's request with the key": func() (*ChangeSetOutput, error) {
			return SubmitFunc(auth.WithActor(context.Background(), s.actors["admin"]), p, params, build)
		},
		"a dry run with the key": func() (*ChangeSetOutput, error) {
			return SubmitFunc(owner, p, ChangeParams{DryRun: true, IdempotencyKey: "site-1"}, build)
		},
		"a request with another key": func() (*ChangeSetOutput, error) {
			return SubmitFunc(owner, p, ChangeParams{IdempotencyKey: "site-2"}, build)
		},
	} {
		builds = 1
		if out, err := try(); !errors.Is(err, errBuiltAgain) || builds != 2 {
			t.Errorf("%s = %+v, %v: it is built as ever", name, out, err)
		}
	}
	if earlier, err := Replayed(owner, p, ChangeParams{DryRun: true, IdempotencyKey: "site-1"}); err != nil || earlier != nil {
		t.Errorf("a dry run is never a replay: %+v, %v", earlier, err)
	}
	if _, err := SubmitFunc(owner, nil, params, build); err == nil {
		t.Error("without a pipeline nothing is submitted")
	}
}
