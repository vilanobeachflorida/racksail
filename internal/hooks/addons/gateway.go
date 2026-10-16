package addons

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
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
		g.api(w, r, actor, grants)
	default:
		writeError(w, i18n.ErrNotFound.New("what", "The path "+r.URL.Path))
	}
}

// api passes a request on to the API as the add-on, and filters what reads
// return down to the fields the add-on may see.
func (g *Gateway) api(w http.ResponseWriter, r *http.Request, actor *auth.Actor, grants Grants) {
	ctx := auth.WithActor(context.WithValue(r.Context(), gatewayKey{}, actor), actor)
	req := r.Clone(ctx)
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	if strings.HasSuffix(r.URL.Path, "/stream") {
		// Live streams only carry notices of changes, not settings.
		g.API.ServeHTTP(w, req)
		return
	}
	rec := &recorder{header: http.Header{}}
	g.API.ServeHTTP(rec, req)
	body := rec.body.Bytes()
	if rec.status < 300 && strings.HasPrefix(rec.header.Get("Content-Type"), "application/json") {
		body = filterBody(body, grants)
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

// filterBody leaves an add-on the fields it may see of the resources and
// change sets an answer holds.
func filterBody(body []byte, g Grants) []byte {
	var v any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return body
	}
	out, err := json.Marshal(filterValue(v, g))
	if err != nil {
		return body
	}
	return out
}

func filterValue(v any, g Grants) any {
	switch x := v.(type) {
	case []any:
		for i := range x {
			x[i] = filterValue(x[i], g)
		}
		return x
	case map[string]any:
		if kind, ok := x["kind"].(string); ok {
			if spec, ok := x["spec"]; ok {
				fields, readable := g.CanRead(kind)
				if !readable {
					delete(x, "spec")
				} else if fields != nil {
					raw, _ := json.Marshal(spec)
					var projected any
					if json.Unmarshal(project(raw, fields), &projected) == nil {
						x["spec"] = projected
					}
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
			if k == "spec" {
				continue
			}
			x[k] = filterValue(child, g)
		}
		return x
	}
	return v
}
