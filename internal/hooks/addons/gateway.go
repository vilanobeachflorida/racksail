package addons

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Errors of the gateway.
var (
	ErrBadToken        = i18n.Define("addons.bad_token", http.StatusUnauthorized, "The request doesn't carry the token of a running add-on.")
	ErrNoSecretsAccess = i18n.Define("addons.no_secrets", http.StatusForbidden, "{addon} wasn't allowed to read secrets.")
)

// gatewayKey carries the add-on the gateway signed in.
type gatewayKey struct{}

// GatewayAuthenticator is the authenticator of the API the gateway serves:
// the add-on the gateway found from its token, and nobody else.
type GatewayAuthenticator struct{}

// Authenticate implements auth.Authenticator.
func (GatewayAuthenticator) Authenticate(ctx context.Context, r *http.Request) (*auth.Actor, error) {
	a, _ := ctx.Value(gatewayKey{}).(*auth.Actor)
	return a, nil
}

// maxResponse bounds the answers the gateway filters.
const maxResponse = 32 << 20

// Gateway is how add-ons reach the panel: the API, with a token that only
// grants what the add-on's permissions allow, and the secrets of what it
// may read, when it was allowed to read secrets. It listens on a socket of
// its own, so add-on tokens are never accepted anywhere else.
type Gateway struct {
	Runtime *Runtime
	// API serves the panel's API for add-ons; its authenticator must be
	// GatewayAuthenticator.
	API     http.Handler
	Secrets secrets.Reader
	Store   store.Reader
}

// ServeHTTP implements http.Handler.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !IsToken(token) {
		writeError(w, ErrBadToken.New())
		return
	}
	inst, err := g.Runtime.opts.Tokens.Lookup(r.Context(), HashToken(token))
	if err != nil {
		writeError(w, ErrBadToken.New())
		return
	}
	resource, man, grants, _, running := g.Runtime.Running(inst)
	if !running {
		writeError(w, ErrBadToken.New())
		return
	}
	actor := grants.Actor(resource, man.ID)
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/secrets/"):
		g.secret(w, r, man.Name, grants, strings.TrimPrefix(r.URL.Path, "/secrets/"))
	case strings.HasPrefix(r.URL.Path, "/api/"):
		g.api(w, r, actor, man.Name, grants)
	default:
		writeError(w, i18n.ErrNotFound.New("what", "The path "+r.URL.Path))
	}
}

// api passes a request on to the API as the add-on, and filters what reads
// return down to what the add-on may see. Lists of a kind it may only see
// part of can't be searched, filtered or sorted by the rest, which would
// tell it what the rest holds.
func (g *Gateway) api(w http.ResponseWriter, r *http.Request, actor *auth.Actor, name string, grants Grants) {
	ctx := auth.WithActor(context.WithValue(r.Context(), gatewayKey{}, actor), actor)
	req := r.Clone(ctx)
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	if strings.HasSuffix(r.URL.Path, "/stream") {
		// Live streams only carry notices of changes, not settings.
		g.API.ServeHTTP(w, req)
		return
	}
	kinds := g.Runtime.kinds()
	about := requestKind(kinds, r.URL.Path)
	if r.Method == http.MethodGet && about != nil && kindList(about.Info(), r.URL.Path) {
		if err := grants.checkQuery(name, about.Info(), r.URL.Query()); err != nil {
			writeError(w, err)
			return
		}
	}
	rec := &recorder{header: http.Header{}}
	g.API.ServeHTTP(rec, req)
	body := rec.body.Bytes()
	if rec.status < 300 && strings.HasPrefix(rec.header.Get("Content-Type"), "application/json") {
		kind := ""
		if about != nil {
			kind = about.Info().Name
		}
		body = filterBody(body, grants, kind)
		if body != nil && r.URL.Path == searchPath {
			body = filterSearch(body, grants)
		}
		if body == nil {
			// An answer that can't be filtered isn't passed on.
			g.Runtime.opts.Log.Error("an API answer to an add-on couldn't be filtered", "addon", name, "path", r.URL.Path)
			writeError(w, i18n.ErrInternal.New())
			return
		}
	}
	for k, v := range rec.header {
		w.Header()[k] = v
	}
	w.Header().Del("Content-Length")
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	w.Write(body)
}

// secret returns the value of one of the secrets of a resource the add-on
// may read, when it was allowed to read secrets.
func (g *Gateway) secret(w http.ResponseWriter, r *http.Request, name string, grants Grants, ref string) {
	if !grants.perms.Secrets {
		writeError(w, ErrNoSecretsAccess.New("addon", name))
		return
	}
	owner, _, err := secrets.Ref(ref).Parse()
	if err != nil {
		writeError(w, err)
		return
	}
	res, err := g.Store.Get(r.Context(), owner)
	if err != nil || res.Deleted() {
		writeError(w, i18n.ErrNotFound.New("what", ref))
		return
	}
	if _, ok := grants.CanRead(res.Kind); !ok || !grants.InScope(res.Scope) {
		writeError(w, i18n.ErrNotFound.New("what", ref))
		return
	}
	value, err := g.Secrets.Get(r.Context(), secrets.Ref(ref))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]string{"ref": ref, "value": string(value)})
}

func writeError(w http.ResponseWriter, err error) {
	p := httpapi.FromError(err)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	json.NewEncoder(w).Encode(p)
}

// recorder keeps an answer to filter it.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.body.Len()+len(b) > maxResponse {
		return 0, http.ErrHandlerTimeout
	}
	return r.body.Write(b)
}

// filterBody leaves an add-on what it may see of the resources and change
// sets an answer holds. kind is the kind the request is about, which specs
// that don't say their own kind, as in a resource's history, belong to.
func filterBody(body []byte, g Grants, kind string) []byte {
	var v any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return nil
	}
	out, err := json.Marshal(filterValue(v, g, kind))
	if err != nil {
		return nil
	}
	return out
}

// filterValue filters the resources and change sets in v. Of a resource
// of a kind the add-on may see all of, it keeps everything; of one it may
// see some fields of, those fields of its spec, the phase of its status
// and, when they're granted, its labels; and of one it may not read at
// all, only which resource it is.
func filterValue(v any, g Grants, kind string) any {
	switch x := v.(type) {
	case []any:
		for i := range x {
			x[i] = filterValue(x[i], g, kind)
		}
		return x
	case map[string]any:
		if spec, ok := x["spec"]; ok {
			k, named := x["kind"].(string)
			if !named {
				k = kind
			}
			fields, readable := g.CanRead(k)
			if k == "" {
				readable = false
			}
			switch {
			case !readable:
				delete(x, "spec")
				delete(x, "status")
				delete(x, "labels")
			case fields != nil:
				raw, _ := json.Marshal(spec)
				var projected any
				if json.Unmarshal(project(raw, fields), &projected) == nil {
					x["spec"] = projected
				} else {
					delete(x, "spec")
				}
				if status, ok := x["status"].(map[string]any); ok {
					x["status"] = map[string]any{"phase": status["phase"]}
				} else {
					delete(x, "status")
				}
				if !g.CanReadLabels(k) {
					delete(x, "labels")
				}
			}
		}
		if _, ok := x["changes"].([]any); ok {
			if _, ok := x["effects"]; ok {
				raw, _ := json.Marshal(x)
				var cs changeset.ChangeSet
				if json.Unmarshal(raw, &cs) == nil {
					filtered, _ := json.Marshal(g.FilterChangeSet(&cs))
					var back map[string]any
					if json.Unmarshal(filtered, &back) == nil {
						return back
					}
				}
			}
		}
		for k, child := range x {
			if k == "spec" || k == "status" || k == "labels" {
				continue
			}
			x[k] = filterValue(child, g, kind)
		}
		return x
	}
	return v
}

// searchPath is the API's search of every kind.
const searchPath = "/api/v1/search"

// filterSearch leaves a search's answer only the results of kinds the
// add-on may see all of. Results show values of title fields, and that a
// resource matched at all says what its other fields hold.
func filterSearch(body []byte, g Grants) []byte {
	var answer map[string]json.RawMessage
	if json.Unmarshal(body, &answer) != nil {
		return nil
	}
	var results []map[string]any
	if json.Unmarshal(answer["results"], &results) != nil {
		return nil
	}
	kept := []map[string]any{}
	for _, r := range results {
		k, _ := r["kind"].(string)
		if fields, ok := g.CanRead(k); ok && fields == nil {
			kept = append(kept, r)
		}
	}
	answer["results"], _ = json.Marshal(kept)
	out, err := json.Marshal(answer)
	if err != nil {
		return nil
	}
	return out
}

// requestKind finds the kind a request to the API is about: the kind of a
// resource its path names, or the kind whose list its path is.
func requestKind(kinds *store.KindRegistry, path string) store.Kind {
	for _, seg := range strings.Split(path, "/") {
		if id, err := store.ParseID(seg); err == nil {
			if k, ok := kinds.ForID(id); ok {
				return k
			}
		}
	}
	for _, k := range kinds.All() {
		if kindList(k.Info(), path) {
			return k
		}
	}
	return nil
}

// kindList reports whether path is where a kind's resources are listed.
func kindList(info *store.KindInfo, path string) bool {
	return strings.TrimSuffix(path, "/") == "/api/v1/"+info.Module()+"/"+info.Plural
}

// checkQuery refuses a list of a kind the add-on may see only some of that
// searches, filters or sorts by anything else.
func (g Grants) checkQuery(name string, info *store.KindInfo, q url.Values) error {
	fields, readable := g.CanRead(info.Name)
	if !readable || fields == nil {
		return nil
	}
	refuse := func(param string) error {
		return ErrQueryDenied.New("addon", name, "kind", info.Name, "param", param)
	}
	if q.Has("q") {
		return refuse("q")
	}
	if q.Has("label") && !g.CanReadLabels(info.Name) {
		return refuse("label")
	}
	sort := strings.TrimPrefix(q.Get("sort"), "-")
	for _, ix := range info.Indexes {
		if pathAllowed(ix.Path, fields) {
			continue
		}
		if q.Has(ix.Name) {
			return refuse(ix.Name)
		}
		if sort == ix.Name {
			return refuse("sort")
		}
	}
	return nil
}
