package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/modules"
)

// Extension adds the core's own operations that live outside this
// package, such as those of the module manager, notifications and
// catalogs: their packages import this one, so it can't import them. They
// register from init functions, and every server adds them after the
// modules' operations and before the kinds'.
type Extension struct {
	// Name orders extensions and must be unique.
	Name string
	// Operations registers documented operations.
	Operations func(api huma.API, svc *modules.Services)
	// Routes are handlers by ServeMux pattern under Prefix, for what an
	// operation can't describe, such as an add-on's own routes, which it
	// documents itself. Requests reach them signed in like any other.
	Routes map[string]func(svc *modules.Services) http.Handler
	// PathCredentials are patterns of Routes whose paths carry what signs
	// a request in, as the wildcard {token}, rather than the session, as
	// the frames of add-ons' screens do. Pages of other origins may send
	// them, since the session cookie isn't what lets them through, and the
	// access log leaves the token out.
	PathCredentials []string
	// Reserved are first path segments under Prefix the extension uses,
	// which no module's kinds may.
	Reserved []string
}

var extensions = struct {
	sync.Mutex
	list []Extension
	errs []error
}{}

// RegisterExtension adds an extension. Call it from an init function.
func RegisterExtension(e Extension) {
	extensions.Lock()
	defer extensions.Unlock()
	for _, other := range extensions.list {
		if other.Name == e.Name {
			extensions.errs = append(extensions.errs, fmt.Errorf("api: extension %s is registered twice", e.Name))
			return
		}
	}
	extensions.list = append(extensions.list, e)
}

// registeredExtensions returns the extensions by name.
func registeredExtensions() []Extension {
	extensions.Lock()
	defer extensions.Unlock()
	out := append([]Extension(nil), extensions.list...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ExtensionErr reports problems found while extensions registered.
func ExtensionErr() error {
	extensions.Lock()
	defer extensions.Unlock()
	return errors.Join(extensions.errs...)
}

// addExtensions registers every extension's operations and routes.
func (s *Server) addExtensions() {
	for _, e := range registeredExtensions() {
		if e.Operations != nil {
			e.Operations(s.api, s.opts.Services)
		}
		patterns := make([]string, 0, len(e.Routes))
		for p := range e.Routes {
			patterns = append(patterns, p)
		}
		sort.Strings(patterns)
		for _, p := range patterns {
			s.mux.Handle(p, e.Routes[p](s.opts.Services))
		}
		for _, p := range e.PathCredentials {
			s.crossOrigin.AddInsecureBypassPattern(p)
			if _, path, ok := strings.Cut(p, " "); ok {
				p = path
			}
			s.credentials = append(s.credentials, strings.Split(strings.Trim(p, "/"), "/"))
		}
	}
}

// loggedPath is a request's path as the access log keeps it, without the
// token of a path that carries one.
func (s *Server) loggedPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for _, pattern := range s.credentials {
		if i, ok := tokenAt(pattern, parts); ok {
			parts[i] = "…"
			return "/" + strings.Join(parts, "/")
		}
	}
	return path
}

// tokenAt returns where a pattern's {token} is among a path's segments,
// when the path is one of the pattern's.
func tokenAt(pattern, parts []string) (int, bool) {
	token := -1
	for i, seg := range pattern {
		if strings.HasSuffix(seg, "...}") {
			return token, token >= 0
		}
		if i >= len(parts) {
			return 0, false
		}
		switch {
		case seg == "{token}":
			token = i
		case strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}"):
		case seg != parts[i]:
			return 0, false
		}
	}
	return token, token >= 0 && len(parts) == len(pattern)
}

// reservedByExtensions are the first path segments extensions use.
func reservedByExtensions() []string {
	var out []string
	for _, e := range registeredExtensions() {
		out = append(out, e.Reserved...)
	}
	return out
}
