package api

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

type searchAnswer struct {
	Results []SearchResult `json:"results"`
	More    []string       `json:"more"`
}

func (s *stack) search(actor, query string) searchAnswer {
	s.t.Helper()
	var out searchAnswer
	if resp := s.call(actor, "GET", "/api/v1/search?"+query, nil, &out); resp.StatusCode != http.StatusOK {
		s.t.Fatalf("search %s as %s = %d %s", query, actor, resp.StatusCode, bodyOf(resp))
	}
	return out
}

func titles(results []SearchResult) []string {
	var out []string
	for _, r := range results {
		out = append(out, r.Title)
	}
	return out
}

func TestSearch(t *testing.T) {
	s := newStack(t, RateLimit{})
	alpha := s.createSite("owner", "alpha.test")
	beta := s.createSite("owner", "beta.test")
	elsewhere := store.AccountScope(store.NewID("acct"))
	var cs changeset.ChangeSet
	s.call("admin", "POST", "/api/v1/test/sites", map[string]any{"scope": elsewhere, "spec": map[string]any{"domains": []string{"alpha-two.test"}, "content": "x"}}, &cs)
	s.finish("admin", &cs)

	// An account's owner finds their own sites only.
	got := s.search("owner", "q=ALPHA")
	if !slices.Equal(titles(got.Results), []string{"alpha.test"}) || got.Results[0].Kind != "test.site" || got.Results[0].ID != alpha ||
		got.Results[0].Phase != store.PhaseReady || got.Results[0].Scope != store.AccountScope(s.account) {
		t.Errorf("the owner's search = %+v", got)
	}
	// An admin finds everyone's, the one the text names exactly first.
	if got := s.search("admin", "q=alpha.test"); !slices.Equal(titles(got.Results), []string{"alpha.test"}) {
		t.Errorf("an exact name = %v", titles(got.Results))
	}
	if got := s.search("admin", "q=alpha"); !slices.Equal(titles(got.Results), []string{"alpha-two.test", "alpha.test"}) {
		t.Errorf("the admin's search = %v", titles(got.Results))
	}
	// An ID finds its resource.
	if got := s.search("owner", "q="+string(alpha)); len(got.Results) != 1 || got.Results[0].ID != alpha {
		t.Errorf("by ID = %+v", got.Results)
	}
	if got := s.search("stranger", "q="+string(alpha)); len(got.Results) != 0 {
		t.Errorf("someone else's ID = %+v", got.Results)
	}
	// A resource being removed is found neither by its ID nor by its name,
	// as lists leave it out, even while what it set up is still there.
	for _, op := range []string{"files.remove", "files.remove_all"} {
		s.machine.FailNext(op, sitePath(beta), errors.New("device busy"))
	}
	var removal changeset.ChangeSet
	if resp := s.call("owner", "DELETE", "/api/v1/test/sites/"+string(beta), nil, &removal); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("delete = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("owner", &removal)
	if r, err := s.store.Get(t.Context(), beta); err != nil || !r.Deleted() {
		t.Fatalf("the site being removed = %+v, %v", r, err)
	}
	for _, q := range []string{"q=" + string(beta), "q=beta"} {
		if got := s.search("owner", q); len(got.Results) != 0 {
			t.Errorf("%s found a site being removed: %+v", q, got.Results)
		}
	}
	// A limit says which kinds have more.
	if got := s.search("admin", "q=.test&limit=1&kind=test.site"); len(got.Results) != 1 || !slices.Equal(got.More, []string{"test.site"}) {
		t.Errorf("limited = %+v", got)
	}
	resp := s.call("admin", "GET", "/api/v1/search?q=x&kind=no.such", nil, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity || s.problem(resp) != i18n.ErrUnknownName.Code {
		t.Errorf("an unknown kind = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := s.call("admin", "GET", "/api/v1/search?q=%20", nil, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("blank text = %d", resp.StatusCode)
	}
	// Someone who may read nothing finds nothing, and nothing is recorded
	// as refused.
	before := len(s.refusals("nobody"))
	if got := s.search("nobody", "q=alpha"); len(got.Results) != 0 || len(got.More) != 0 {
		t.Errorf("nobody's search = %+v", got)
	}
	if after := len(s.refusals("nobody")); after != before {
		t.Errorf("the search recorded %d refusals", after-before)
	}
}

func TestTextAt(t *testing.T) {
	spec := []byte(`{"domains":["a.test","b.test"],"tls":{"mode":"auto"},"port":8080,"on":true,"empty":[]}`)
	for path, want := range map[string]string{
		"domains": "a.test", "tls.mode": "auto", "port": "8080", "on": "true", "empty": "", "tls": "", "missing.deep": "", "": "",
	} {
		if got := textAt(spec, path); got != want {
			t.Errorf("textAt(%q) = %q, want %q", path, got, want)
		}
	}
}
