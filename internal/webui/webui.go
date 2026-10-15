package webui

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// SecurityPolicy is the Content Security Policy of the UI. The UI makes no
// requests to other origins: fonts and images are bundled.
const SecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
	"font-src 'self'; connect-src 'self'; worker-src 'self' blob:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// Assets returns the built UI, or false when the binary was built without it.
func Assets() (fs.FS, bool) {
	sub, err := fs.Sub(dist, "dist/app")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}

// Handler serves the UI from assets, or the fallback page when assets is nil.
func Handler(assets fs.FS) http.Handler {
	if assets == nil {
		return http.HandlerFunc(fallback)
	}
	files := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setHeaders(w)
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if fi, err := fs.Stat(assets, name); err == nil && !fi.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Vite puts a content hash in every asset name.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, assets, "index.html")
	})
}

func setHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", SecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

var fallbackPage = template.Must(template.New("fallback").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Racksail</title>
<style>
  :root { color-scheme: light dark; --bg: #F7F9FC; --fg: #0B1B33; --muted: #4A5A70; --accent: #0062CC; --code: #EEF2F7; }
  @media (prefers-color-scheme: dark) { :root { --bg: #0A1626; --fg: #E6EDF5; --muted: #9FB0C4; --accent: #3D9BFF; --code: #0F2036; } }
  body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: var(--bg); color: var(--fg);
         font-family: Inter, ui-sans-serif, system-ui, sans-serif; line-height: 1.5; }
  main { max-width: 34rem; padding: 2rem 1rem; }
  h1 { font-size: 1.5rem; margin: 0 0 0.5rem; }
  p { color: var(--muted); }
  code { font-family: "JetBrains Mono", ui-monospace, monospace; background: var(--code); padding: 0.1rem 0.35rem; border-radius: 4px; }
  a { color: var(--accent); }
</style>
</head>
<body>
<main>
  <h1>Racksail is running</h1>
  <p>This binary was built without the web UI, so there is nothing to show here yet. The API is up at <a href="/api/v1/health">/api/v1/health</a>.</p>
  <p>To include the UI, run <code>make build</code>. While developing, <code>make dev</code> serves the UI with hot reload.</p>
</main>
</body>
</html>
`))

func fallback(w http.ResponseWriter, r *http.Request) {
	setHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != "/" && path.Ext(r.URL.Path) != "" {
		http.NotFound(w, r)
		return
	}
	if err := fallbackPage.Execute(w, nil); err != nil {
		http.Error(w, "the fallback page failed to render", http.StatusInternalServerError)
	}
}
