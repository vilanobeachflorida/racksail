// Package webui embeds the built web UI in the binary and serves it.
//
// The UI build (npm --prefix ui run build) writes to dist/app. Hashed assets
// are served with long cache lifetimes and every other path gets index.html,
// so the single-page app's routes work on reload. A binary built before the
// UI shows a page explaining how to build it.
package webui
