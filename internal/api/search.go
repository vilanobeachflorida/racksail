package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// SearchResult is a resource a search found, with what screens show of it
// in a list.
type SearchResult struct {
	ID        store.ID    `json:"id" doc:"The resource's ID"`
	Kind      string      `json:"kind" doc:"The resource's kind, such as web.site"`
	Scope     store.Scope `json:"scope" doc:"The owner the resource belongs to"`
	Title     string      `json:"title" doc:"The value of the kind's title field, or the ID when the kind has none"`
	Subtitle  string      `json:"subtitle,omitempty" doc:"The value of the kind's subtitle field"`
	Phase     store.Phase `json:"phase" enum:"pending,applying,ready,degraded,failed,deleting" doc:"Where the resource is: being put in place, ready, failing or being deleted"`
	UpdatedAt time.Time   `json:"updatedAt" doc:"When the resource last changed"`
}

type searchInput struct {
	Q     string   `query:"q" required:"true" minLength:"1" maxLength:"200" doc:"The text to look for in IDs, titles, subtitles and indexed fields, in any case"`
	Kinds []string `query:"kind" explode:"true" doc:"Only resources of these kinds; repeat it for several"`
	Limit int      `query:"limit" minimum:"0" maximum:"50" doc:"How many results of each kind; 5 when left out"`
}

type searchOutput struct {
	Body struct {
		Results []SearchResult `json:"results" doc:"The resources that match"`
		// More names the kinds that had more results than the limit.
		More []string `json:"more,omitempty" doc:"Kinds with more results than the limit; search one of them alone, with a higher limit, for the rest"`
	}
}

// defaultSearchLimit is how many results of each kind a search returns
// unless asked for more: a command bar shows a few of each.
const defaultSearchLimit = 5

// textAt returns the text at a dotted path of a spec: the value itself, or
// the first item of a list, as titles and subtitles show it.
func textAt(spec json.RawMessage, path string) string {
	if path == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal(spec, &v); err != nil {
		return ""
	}
	for _, key := range strings.Split(path, ".") {
		obj, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = obj[key]
	}
	if list, ok := v.([]any); ok {
		if len(list) == 0 {
			return ""
		}
		v = list[0]
	}
	switch t := v.(type) {
	case string:
		return t
	case float64, bool:
		b, _ := json.Marshal(t)
		return string(b)
	}
	return ""
}

// searchResultOf describes a resource for a list of results.
func searchResultOf(info *store.KindInfo, r *store.Resource) SearchResult {
	res := SearchResult{
		ID: r.ID, Kind: r.Kind, Scope: r.Scope, Phase: store.PhaseOf(r), UpdatedAt: r.UpdatedAt,
		Title: textAt(r.Spec, info.UI.TitleField), Subtitle: textAt(r.Spec, info.UI.SubtitleField),
	}
	if res.Title == "" {
		res.Title = string(r.ID)
	}
	return res
}

// searchRank orders results: the resource the text names exactly, then
// those whose title starts with it, then the rest.
func searchRank(q string, r SearchResult) int {
	switch {
	case strings.EqualFold(string(r.ID), q) || strings.EqualFold(r.Title, q):
		return 0
	case strings.HasPrefix(strings.ToLower(r.Title), strings.ToLower(q)):
		return 1
	}
	return 2
}

func (s *Server) registerSearch() {
	op := huma.Operation{
		OperationID: "searchResources", Method: http.MethodGet, Path: Prefix + "/search", Tags: []string{"System"}, Security: Protected,
		Summary: "Search resources of every kind",
		Description: "Finds the resources you can see, of every kind or of the kinds you name, whose ID, title, subtitle or indexed fields contain the text. " +
			"Results come a few of each kind, best first: the resource the text names exactly, then those whose title starts with it. " +
			"The panel's command bar searches with it.",
	}
	// The answer only holds what the caller may read, so it needs nothing
	// more than a sign-in, like the lists of jobs and change sets.
	Describe(&op, Info{Operation: "search", Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *searchInput) (*searchOutput, error) {
		if _, err := Actor(ctx); err != nil {
			return nil, err
		}
		st := s.store()
		if st == nil {
			return nil, i18n.ErrUnavailable.New("what", "The store")
		}
		q := strings.TrimSpace(in.Q)
		if q == "" {
			return nil, badParam("q", "give some text to look for")
		}
		limit := in.Limit
		if limit <= 0 {
			limit = defaultSearchLimit
		}
		var kinds []store.Kind
		for _, name := range in.Kinds {
			k, ok := s.kinds.Get(name)
			if !ok {
				return nil, i18n.ErrUnknownName.New("what", "kind", "name", name)
			}
			kinds = append(kinds, k)
		}
		if len(kinds) == 0 {
			kinds = s.kinds.All()
		}
		out := &searchOutput{}
		out.Body.Results = []SearchResult{}
		seen := map[store.ID]bool{}
		// The text may be an ID itself, as in a link someone pasted. A
		// resource being removed is left out, as lists and the text search
		// leave it out.
		if id, err := store.ParseID(q); err == nil {
			if k, ok := s.kinds.ForID(id); ok && !k.Info().UI.Hidden && slices.Contains(kinds, k) {
				if r, err := st.Get(ctx, id); err == nil && !r.Deleted() && r.Kind == k.Info().Name && canRead(ctx, s.authorizer(), r.Meta) {
					out.Body.Results = append(out.Body.Results, searchResultOf(k.Info(), r))
					seen[r.ID] = true
				}
			}
		}
		for _, k := range kinds {
			info := k.Info()
			if info.UI.Hidden {
				continue
			}
			page, err := list(ctx, s.authorizer(), st, store.Query{Kind: info.Name, Search: q, Limit: limit + 1}, false)
			if i18n.ErrForbidden.Is(err) {
				// Kinds the caller can't read are left out, as lists leave
				// out resources they can't see.
				continue
			}
			if err != nil {
				return nil, err
			}
			items := page.Items
			if len(items) > limit || page.Next != "" {
				out.Body.More = append(out.Body.More, info.Name)
				items = items[:min(limit, len(items))]
			}
			for _, r := range items {
				if !seen[r.ID] {
					seen[r.ID] = true
					out.Body.Results = append(out.Body.Results, searchResultOf(info, r))
				}
			}
		}
		order := map[string]int{}
		for i, k := range kinds {
			order[k.Info().Name] = i
		}
		slices.SortStableFunc(out.Body.Results, func(a, b SearchResult) int {
			if d := searchRank(q, a) - searchRank(q, b); d != 0 {
				return d
			}
			if d := order[a.Kind] - order[b.Kind]; d != 0 {
				return d
			}
			return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
		})
		return out, nil
	})
}
