// Command addon is the OpenID Connect sign-in add-on. It signs people in
// on an OpenID Connect provider's own page, such as Keycloak, Authentik,
// Microsoft Entra ID, Google or Okta, with the authorization code flow and
// PKCE, and tells the panel who they are there.
//
// The panel starts it from its racksail-addon@ socket and calls the
// auth-provider capability's describe, begin and complete methods with the
// provider's settings. Run by hand, it listens on the socket --socket
// names.
package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/vilanobeachflorida/racksail/addons/internal/serve"
)

// version is set when releases are built.
var version = "0.0.0-dev"

func main() {
	socket := flag.String("socket", "", "the Unix socket to listen on when systemd doesn't pass one")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := serve.Run(addon(log), *socket); err != nil {
		log.Error("the add-on stopped", "error", err)
		os.Exit(1)
	}
}

func addon(log *slog.Logger) *serve.Addon {
	p := newProvider()
	return &serve.Addon{
		Name:    "OpenID Connect sign-in",
		Version: version,
		Log:     log,
		Capabilities: map[string]map[string]serve.Method{
			"auth-provider": {"describe": p.describe, "begin": p.begin, "complete": p.complete},
		},
	}
}
