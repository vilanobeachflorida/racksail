package addons

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// Frames are how the panel shows add-ons' screens. Each time someone opens
// one, the frame the panel shows it in gets an address of its own,
// /api/v1/addons/<id>/frames/<token>/, whose token stands for that person
// with that add-on and nothing else. The frame is sandboxed without the
// panel's origin, so its page never reaches the panel's session, cookies or
// pages, and the session cookie doesn't reach the frame either: what its
// page loads and asks for under its address, the add-on's screens under
// ui/ and its routes under routes/, is signed in by the token instead.

const (
	// FrameIdle is how long a frame's address works after its last
	// request, and FrameLifetime how long it works at most.
	FrameIdle     = 30 * time.Minute
	FrameLifetime = 12 * time.Hour
	// maxFrames bounds the frames kept; the least recently used go first.
	maxFrames = 4096
)

// ErrFrameGone answers a request at a frame's address once it stopped
// working, or for an add-on that no longer runs.
var ErrFrameGone = i18n.Define("addons.frame_gone", http.StatusNotFound, "This screen of {addon} was left too long, or the add-on stopped. Open it again from the panel.")

// frame is one screen someone opened.
type frame struct {
	instance     string
	actor        *auth.Actor
	opened, used time.Time
}

// frames are the frames open, by the hash of their token, which is all the
// panel keeps of it.
type frames struct {
	mu   sync.Mutex
	open map[string]*frame
	now  func() time.Time
}

func frameKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// FrameBase is the address of a frame of an add-on's screens, ending in a
// slash: its screens are at <base>ui/<path> and the add-on's routes at
// <base>routes/<path>.
func FrameBase(inst, token string) string {
	return "/api/v1/addons/" + url.PathEscape(inst) + "/frames/" + token + "/"
}

// OpenFrame opens a frame of a running add-on's screens for the person who
// asked to see one, and returns its address, FrameBase.
func (r *Runtime) OpenFrame(inst string, actor *auth.Actor) (string, error) {
	if actor == nil {
		return "", i18n.ErrUnauthenticated.New()
	}
	if _, _, _, _, ok := r.Running(inst); !ok {
		return "", ErrUnknown.New("addon", inst)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	f := &r.frames
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	var oldest string
	for key, fr := range f.open {
		if fr.done(now) {
			delete(f.open, key)
		} else if oldest == "" || fr.used.Before(f.open[oldest].used) {
			oldest = key
		}
	}
	if len(f.open) >= maxFrames {
		delete(f.open, oldest)
	}
	f.open[frameKey(token)] = &frame{instance: inst, actor: actor.Clone(), opened: now, used: now}
	return FrameBase(inst, token), nil
}

func (fr *frame) done(now time.Time) bool {
	return now.Sub(fr.used) > FrameIdle || now.Sub(fr.opened) > FrameLifetime
}

// frameActor returns who a frame of an add-on's was opened for, while its
// address works, and counts the request as a use of it.
func (r *Runtime) frameActor(inst, token string) (*auth.Actor, bool) {
	f := &r.frames
	f.mu.Lock()
	defer f.mu.Unlock()
	key := frameKey(token)
	fr, ok := f.open[key]
	if !ok || fr.instance != inst {
		return nil, false
	}
	now := f.now()
	if fr.done(now) {
		delete(f.open, key)
		return nil, false
	}
	fr.used = now
	return fr.actor.Clone(), true
}

// FrameOf returns who a frame was opened for, while its address works,
// without counting it as a use: the panel's API asks before the request
// reaches the frame, to count it against that person's rate limit.
func (r *Runtime) FrameOf(token string) *auth.Actor {
	f := &r.frames
	f.mu.Lock()
	defer f.mu.Unlock()
	fr, ok := f.open[frameKey(token)]
	if !ok || fr.done(f.now()) {
		return nil
	}
	return fr.actor.Clone()
}

// closeFrame closes one frame, once whoever it was opened for may no
// longer use its add-on.
func (r *Runtime) closeFrame(token string) {
	f := &r.frames
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.open, frameKey(token))
}

// closeFrames closes every frame of an add-on's screens, as it stops.
func (r *Runtime) closeFrames(inst string) {
	f := &r.frames
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, fr := range f.open {
		if fr.instance == inst {
			delete(f.open, key)
		}
	}
}

// frameScreenPolicy is what a screen may do in its frame: run its own
// scripts, those it serves and those inline with the nonce of the answer,
// load its own styles, images and fonts and call its own routes, and
// nothing of the panel's.
func frameScreenPolicy(nonce string) string {
	return "sandbox allow-scripts allow-forms; default-src 'none'; script-src 'self' 'nonce-" + nonce + "'; " +
		"style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; " +
		"form-action 'self'; frame-ancestors 'self'; base-uri 'none'"
}

// Frames returns the handler of the frames of add-ons' screens, for a
// ServeMux pattern ending in /api/v1/addons/{addon}/frames/{token}/{path...}.
// The token signs requests in, whatever session they carry, while allowed,
// when given, says the person it was opened for may still use the add-on;
// pages of any origin may load what it serves, since the frame's own has
// none.
func (r *Runtime) Frames(allowed func(ctx context.Context, actor *auth.Actor, inst string) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		inst, token := req.PathValue("addon"), req.PathValue("token")
		actor, ok := r.frameActor(inst, token)
		r.mu.Lock()
		in := r.running[inst]
		r.mu.Unlock()
		if ok && allowed != nil && !allowed(req.Context(), actor, inst) {
			r.closeFrame(token)
			ok = false
		}
		if !ok || in == nil {
			frameProblem(w, ErrFrameGone.New("addon", inst))
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Cross-Origin-Resource-Policy", "cross-origin")
		h.Set("Referrer-Policy", "no-referrer")
		h.Del("X-Frame-Options")
		area, rest, _ := strings.Cut(req.PathValue("path"), "/")
		req = req.WithContext(auth.WithActor(req.Context(), actor))
		req.SetPathValue("path", rest)
		base := FrameBase(inst, token)
		switch area {
		case "ui":
			r.frameScreen(w, req, in, base)
		case "routes":
			r.frameRoute(w, req, in)
		default:
			frameProblem(w, i18n.ErrNotFound.New("what", "Page "+area+"/"+rest))
		}
	})
}

// frameScreen serves one of an add-on's screens in its frame, telling the
// add-on who is looking, where the frame is and the nonce its inline
// scripts run with.
func (r *Runtime) frameScreen(w http.ResponseWriter, req *http.Request, in *installed, base string) {
	switch req.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodOptions:
		preflight(w, http.MethodGet)
		return
	default:
		w.Header().Set("Allow", "GET, HEAD")
		frameProblem(w, i18n.ErrMethodNotAllowed.New("path", req.URL.Path, "method", req.Method, "allowed", "GET"))
		return
	}
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		frameProblem(w, err)
		return
	}
	nonce := base64.StdEncoding.EncodeToString(raw)
	w.Header().Set("Content-Security-Policy", frameScreenPolicy(nonce))
	in.client.Proxy("/ui/", func(req *http.Request) http.Header {
		h := actorHeaders(req)
		h.Set("Racksail-Frame-Base", base)
		h.Set("Racksail-Nonce", nonce)
		return h
	}).ServeHTTP(w, req)
}

// frameRoute passes a request from a frame's page on to one of the routes
// the add-on declares, as the person the frame was opened for.
func (r *Runtime) frameRoute(w http.ResponseWriter, req *http.Request, in *installed) {
	path := "/" + req.PathValue("path")
	method := req.Method
	if method == http.MethodOptions {
		method = req.Header.Get("Access-Control-Request-Method")
	}
	declared := false
	for _, rt := range in.manifest.Routes {
		declared = declared || rt.Matches(method, path)
	}
	switch {
	case !declared:
		frameProblem(w, i18n.ErrNotFound.New("what", req.Method+" "+path))
	case req.Method == http.MethodOptions:
		preflight(w, method)
	default:
		in.client.Proxy("/routes/", actorHeaders).ServeHTTP(w, req)
	}
}

// preflight answers a browser asking whether a page of another origin, as
// a frame's is, may send a request.
func preflight(w http.ResponseWriter, method string) {
	h := w.Header()
	h.Set("Access-Control-Allow-Methods", method)
	h.Set("Access-Control-Allow-Headers", "Accept, Accept-Language, Content-Type")
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

// frameProblem answers a frame's request with an error.
func frameProblem(w http.ResponseWriter, err error) {
	p := httpapi.FromError(err)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	json.NewEncoder(w).Encode(p)
}
