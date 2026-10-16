package edge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// A route is compiled into Caddy handlers in a context of its own, so
// replacing or removing it cleans up exactly what it started, such as
// health checks, without touching the rest of the configuration.

// compiledRoute is a route ready to serve.
type compiledRoute struct {
	route   Route
	hash    string
	handler caddyhttp.Handler
	cancel  func()
}

func routeHash(r Route) string {
	b, _ := json.Marshal(r)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:8])
}

// rootFS serves files inside a directory without following links out of
// it. Each call opens the directory anew, so a site whose root is a link
// to its current release sees the new release as soon as the link moves.
// The directory itself may be such a link, which its account controls, so
// what was opened is checked against the edge's own files, which are
// never served: its certificates and keys, and the uploaded certificates
// it reads.
type rootFS struct {
	dir       string
	protected []string
}

func (r rootFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	f, err := os.OpenInRoot(r.dir, name)
	if err != nil {
		return nil, err
	}
	if len(r.protected) > 0 {
		real, err := openedPath(f)
		if err != nil || insideAny(real, r.protected) {
			f.Close()
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
	}
	return f, nil
}

// insideAny reports whether p is one of dirs or below one.
func insideAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if within(p, d) {
			return true
		}
	}
	return false
}

func within(p, dir string) bool {
	return dir == "/" || p == dir || strings.HasPrefix(p, dir+"/")
}

// Stat implements fs.StatFS.
func (r rootFS) Stat(name string) (fs.FileInfo, error) {
	f, err := r.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

// hiddenFiles are never served from a static root.
var hiddenFiles = []string{".git", ".env", ".htaccess", ".htpasswd", ".DS_Store"}

// module is a Caddy handler module as JSON.
func module(name string, fields obj) json.RawMessage {
	o := obj{"handler": name}
	maps.Copy(o, fields)
	b, err := json.Marshal(o)
	if err != nil {
		// Only values this package builds go in; they always encode.
		panic(err)
	}
	return b
}

func matchers(sets ...obj) []caddy.ModuleMap {
	var out []caddy.ModuleMap
	for _, set := range sets {
		m := caddy.ModuleMap{}
		for k, v := range set {
			b, err := json.Marshal(v)
			if err != nil {
				panic(err)
			}
			m[k] = b
		}
		out = append(out, m)
	}
	return out
}

func seconds(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s" }

// escapePlaceholders keeps Caddy from expanding placeholders in a value
// that comes from a route, such as a header value: they would read files
// ({file.*}), the environment ({env.*}) and the request. Caddy reads \{ and
// \} as plain braces and leaves every other backslash alone.
func escapePlaceholders(v string) string {
	return strings.NewReplacer("{", `\{`, "}", `\}`).Replace(v)
}

// replaceURI turns the {uri} of a redirect target into Caddy's
// placeholder; every other brace is escaped.
func replaceURI(to string) string {
	return strings.ReplaceAll(escapePlaceholders(to), `\{uri\}`, "{http.request.uri}")
}

// upstreamDial is the address Caddy dials.
func upstreamDial(u Upstream) string {
	if u.Network == "unix" {
		return "unix/" + u.Address
	}
	return u.Address
}

// proxyHandler builds a reverse proxy over the route's upstreams with the
// given transport.
func proxyHandler(h Handler, transport obj) json.RawMessage {
	var upstreams []obj
	var weights []int
	weighted := false
	for _, u := range h.Upstreams {
		upstreams = append(upstreams, obj{"dial": upstreamDial(u)})
		w := u.Weight
		if w == 0 {
			w = 1
		} else {
			weighted = true
		}
		weights = append(weights, w)
	}
	lb := obj{"selection_policy": obj{"policy": "round_robin"}}
	if weighted {
		lb["selection_policy"] = obj{"policy": "weighted_round_robin", "weights": weights}
	}
	// A sleeping upstream behind socket activation accepts the connection
	// at once and answers once it has started.
	if h.WakeTimeout > 0 {
		lb["try_duration"] = seconds(h.WakeTimeout)
		lb["try_interval"] = "250ms"
	} else if len(upstreams) > 1 {
		lb["try_duration"] = "5s"
	}
	fields := obj{
		"upstreams":      upstreams,
		"load_balancing": lb,
		"transport":      transport,
	}
	health := obj{}
	if len(upstreams) > 1 {
		health["passive"] = obj{"fail_duration": "30s", "max_fails": 2}
	}
	if h.HealthPath != "" {
		health["active"] = obj{"uri": h.HealthPath, "interval": "15s", "timeout": "5s"}
	}
	if len(health) > 0 {
		fields["health_checks"] = health
	}
	return module("reverse_proxy", fields)
}

// compile turns a route into Caddy routes. fsName is the file system its
// static or FastCGI root is registered under.
func (e *Edge) compile(r Route, fsName string) (caddyhttp.RouteList, error) {
	h := r.Handler
	var handlers []json.RawMessage
	if len(r.Headers) > 0 {
		set := http.Header{}
		for _, hd := range r.Headers {
			set.Add(hd.Name, escapePlaceholders(hd.Value))
		}
		handlers = append(handlers, module("headers", obj{"response": obj{"set": set, "deferred": true}}))
	}
	compressible := h.Type == HandleStatic || h.Type == HandleProxy || h.Type == HandleFastCGI
	if compressible {
		handlers = append(handlers, module("encode", obj{
			"encodings":      obj{"zstd": obj{}, "gzip": obj{}},
			"prefer":         []string{"zstd", "gzip"},
			"minimum_length": 512,
		}))
	}
	if h.StripPath && r.Path != "" && r.Path != "/" && (h.Type == HandleProxy || h.Type == HandleStatic) {
		handlers = append(handlers, module("rewrite", obj{"strip_path_prefix": r.Path}))
	}

	switch h.Type {
	case HandleStatic:
		server := module("file_server", obj{
			"fs": fsName, "root": ".", "index_names": []string{"index.html", "index.htm"}, "hide": hiddenFiles,
		})
		if h.Fallback == "" {
			handlers = append(handlers, server)
			break
		}
		// Paths that aren't files are served the fallback, as
		// applications that route in the browser need.
		handlers = append(handlers, module("subroute", obj{"routes": caddyhttp.RouteList{
			{
				MatcherSetsRaw: matchers(obj{"not": []obj{{"file": obj{"fs": fsName, "root": ".", "try_files": []string{"{http.request.uri.path}", "{http.request.uri.path}/"}}}}}),
				HandlersRaw:    []json.RawMessage{module("rewrite", obj{"uri": h.Fallback})},
			},
			{HandlersRaw: []json.RawMessage{server}},
		}}))
	case HandleProxy:
		transport := obj{"protocol": "http", "dial_timeout": "5s"}
		if h.Upstreams[0].TLS {
			// Validation keeps HTTPS upstreams on loopback addresses.
			transport["tls"] = obj{"insecure_skip_verify": true}
		}
		if h.WakeTimeout > 0 {
			transport["response_header_timeout"] = seconds(h.WakeTimeout + 30*time.Second)
		}
		handlers = append(handlers, proxyHandler(h, transport))
	case HandleFastCGI:
		handlers = append(handlers, e.fastCGI(h, fsName)...)
	case HandleRedirect:
		code := h.Code
		if code == 0 {
			code = http.StatusPermanentRedirect
		}
		handlers = append(handlers, module("static_response", obj{
			"status_code": strconv.Itoa(code),
			"headers":     http.Header{"Location": {replaceURI(h.To)}},
		}))
	case HandleRespond:
		code := h.Code
		if code == 0 {
			code = http.StatusOK
		}
		handlers = append(handlers, module("static_response", obj{
			"status_code": strconv.Itoa(code),
			"headers":     http.Header{"Content-Type": {"text/plain; charset=utf-8"}},
			"body":        escapePlaceholders(h.Body),
		}))
	default:
		return nil, errors.New("unknown handler type " + string(h.Type))
	}
	return caddyhttp.RouteList{{HandlersRaw: handlers}}, nil
}

// fastCGI builds what Caddy's php_fastcgi does: directories redirect to
// their canonical path when they hold the index, missing files go to the
// front controller, and scripts go to FastCGI.
func (e *Edge) fastCGI(h Handler, fsName string) []json.RawMessage {
	index := h.Index
	if index == "" {
		index = "index.php"
	}
	split := h.SplitPath
	if len(split) == 0 {
		split = []string{".php"}
	}
	transport := obj{
		"protocol": "fastcgi", "root": h.Root, "split_path": split,
		"dial_timeout": "5s", "read_timeout": "300s", "write_timeout": "300s",
	}
	if len(h.Env) > 0 {
		env := make(map[string]string, len(h.Env))
		for k, v := range h.Env {
			env[k] = escapePlaceholders(v)
		}
		transport["env"] = env
	}
	var scripts []string
	for _, ext := range split {
		scripts = append(scripts, "*"+ext)
	}
	dirIndex := "{http.request.uri.path}/" + index
	routes := caddyhttp.RouteList{
		{
			MatcherSetsRaw: matchers(obj{
				"file": obj{"try_files": []string{dirIndex}},
				"not":  []obj{{"path": []string{"*/"}}},
			}),
			HandlersRaw: []json.RawMessage{module("static_response", obj{
				"status_code": strconv.Itoa(http.StatusPermanentRedirect),
				"headers":     http.Header{"Location": {"{http.request.orig_uri.path}/{http.request.orig_uri.prefixed_query}"}},
			})},
		},
		{
			MatcherSetsRaw: matchers(obj{"file": obj{
				"try_files": []string{"{http.request.uri.path}", dirIndex, index}, "try_policy": "first_exist_fallback", "split_path": split,
			}}),
			HandlersRaw: []json.RawMessage{module("rewrite", obj{"uri": "{http.matchers.file.relative}"})},
		},
		{
			MatcherSetsRaw: matchers(obj{"path": scripts}),
			HandlersRaw:    []json.RawMessage{proxyHandler(h, transport)},
		},
	}
	return []json.RawMessage{
		// The file matchers look in the edge's view of the root; FastCGI is
		// given the root as the PHP processes see it.
		module("vars", obj{"root": ".", "fs": fsName}),
		module("subroute", obj{"routes": routes}),
		// What isn't a script, such as stylesheets and images next to the
		// scripts, is served as a file.
		module("file_server", obj{"fs": fsName, "root": ".", "hide": hiddenFiles}),
	}
}

// staticRoot is the directory a route's files come from, as the edge
// reads it.
func (e *Edge) staticRoot(r Route) string {
	if r.Handler.Type != HandleStatic && r.Handler.Type != HandleFastCGI {
		return ""
	}
	return e.mapPath(r.Handler.Root)
}

// fsKey names a route's file system in Caddy's registry.
func fsKey(r Route, hash string) string {
	return "racksail-" + strings.ReplaceAll(r.key(), "/", "-") + "-" + hash
}
