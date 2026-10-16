package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/cli"
	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/modules"
)

// Every operation of the binary that API tokens may use has a command,
// modules' included, and the ones only people in the panel may use have
// none.
func TestEveryOperationHasACommand(t *testing.T) {
	root := newRootCommand()
	if root.PersistentPreRunE != nil {
		t.Fatal(root.PersistentPreRunE(root, nil))
	}
	raw, err := api.New(api.Options{Modules: modules.Default(), Services: &modules.Services{}}).OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := cli.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range doc.Operations {
		words := cli.CommandPath(op)
		cmd, rest, err := root.Find(words)
		found := err == nil && len(rest) == 0 && cmd.Name() == words[len(words)-1]
		switch {
		case op.Session && found && cmd.Name() != "whoami":
			t.Errorf("%s is only for people in the panel, but racksail %s calls it", op.ID, strings.Join(words, " "))
		case !op.Session && !found:
			t.Errorf("%s has no command: racksail %s", op.ID, strings.Join(words, " "))
		case !op.Session && op.Mutating() && !op.Stream && cmd.Flags().Lookup("dry-run") == nil:
			t.Errorf("racksail %s changes something but has no --dry-run", strings.Join(words, " "))
		}
	}
}

// A group says what it holds, whichever of its commands made it first:
// a kind's group is named after the kind, as it reads in a sentence.
func TestCommandGroupsSayWhatTheyHold(t *testing.T) {
	root := newRootCommand()
	for name, want := range map[string]string{
		"addon":                    "Manage add-ons",
		"catalog":                  "Manage catalogs",
		"installation":             "Manage installed apps",
		"update-settings":          "Manage update settings",
		"notification-preferences": "Manage notification preferences",
		"inbox":                    "See what needs your attention, and dismiss it",
	} {
		c, _, err := root.Find([]string{name})
		if err != nil || c.Name() != name {
			t.Errorf("racksail %s isn't a command: %v", name, err)
			continue
		}
		if c.Short != want {
			t.Errorf("racksail %s says %q, want %q", name, c.Short, want)
		}
	}
	if c, _, err := root.Find([]string{"inboxes"}); err != nil || c.Name() != "inbox" {
		t.Error("racksail inboxes isn't racksail inbox")
	}
}

// The roles that run Racksail skip building the commands from the API.
func TestRolesSkipTheAPICommands(t *testing.T) {
	has := func(root *cobra.Command, name string) bool {
		c, _, err := root.Find([]string{name})
		return err == nil && c.Name() == name
	}
	if root := newRootCommandFor([]string{"agent", "--sim"}); has(root, "webhook") || !has(root, "agent") {
		t.Error("racksail agent built the API's commands")
	}
	if root := newRootCommandFor([]string{"webhook", "list"}); !has(root, "webhook") {
		t.Error("racksail webhook list has no command")
	}
	if root := newRootCommandFor(nil); !has(root, "account") || !has(root, "login") {
		t.Error("racksail on its own lists no client commands")
	}
	// Every command that runs Racksail is one of the roles.
	root := newRootCommand()
	for _, cmd := range root.Commands() {
		if cmd.GroupID == groupServer && !slices.Contains(roles, cmd.Name()) {
			t.Errorf("racksail %s runs Racksail but isn't in roles", cmd.Name())
		}
	}
	for _, name := range roles {
		if c, _, err := root.Find([]string{name}); err != nil || c.Name() != name || c.GroupID != groupServer {
			t.Errorf("%s is in roles but isn't a command that runs Racksail", name)
		}
	}
}

// The commands work against a panel: they sign in, preview and make
// changes, follow the jobs that carry them out and read the results.
func TestCommandsAgainstAPanel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := shortTemp(t)
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv(cli.ConfigEnv, filepath.Join(dir, "cli.json"))
	for _, env := range []string{cli.ServerEnv, cli.TokenEnv, cli.ProfileEnv} {
		os.Unsetenv(env)
	}
	cfg, err := config.Dev(dir, config.DevAddresses{Panel: "127.0.0.1:0", EdgeHTTP: "127.0.0.1:0", EdgeHTTPS: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := newDevServer(ctx, cfg, dir, false, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer d.close(context.Background())
	dev, err := d.core.identity.Developer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	apiServer, err := d.core.apiServer(identity.DevAuthenticator{Service: d.core.identity, User: dev}, true)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(apiServer.Handler())
	defer srv.Close()
	defer apiServer.Close()

	// An API token, made in the panel, signs the command in.
	actor, err := d.core.identity.Actor(ctx, dev, auth.Client{IP: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := d.core.identity.CreateToken(ctx, actor, identity.TokenInput{Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	cmdIn := func(stdin string, args ...string) (string, error) {
		root := newRootCommand()
		var out strings.Builder
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetIn(strings.NewReader(stdin))
		root.SetArgs(args)
		err := root.ExecuteContext(ctx)
		return out.String(), err
	}
	if out, err := cmdIn(token.Token+"\n", "login", "--server", srv.URL, "--token-stdin", "--profile", "dev"); err != nil {
		t.Fatalf("login = %s, %v", out, err)
	}
	out, err := cmdIn("", "whoami")
	if err != nil || !strings.Contains(out, `the API token "laptop"`) {
		t.Fatalf("whoami = %s, %v", out, err)
	}

	out, err = cmdIn("", "account", "open", "--slug", "acme", "--name", "Acme Inc.", "--limits-memory-max", "1G", "--owner-username", "acmeowner", "--dry-run")
	if err != nil || !strings.Contains(out, "This is a preview") || !strings.Contains(out, "racksail-accounts-acme.slice") {
		t.Fatalf("preview = %s, %v", out, err)
	}
	out, err = cmdIn("", "account", "open", "--slug", "acme", "--name", "Acme Inc.", "--limits-memory-max", "1G", "--owner-username", "acmeowner")
	if err != nil || !strings.Contains(out, "Following job") || !strings.Contains(out, "Done") || !strings.Contains(out, "acmeowner") {
		t.Fatalf("open = %s, %v", out, err)
	}
	out, err = cmdIn("", "account", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []struct {
			ID   string `json:"id"`
			Spec struct {
				Slug   string `json:"slug"`
				Limits struct {
					MemoryMax int64 `json:"memoryMax"`
				} `json:"limits"`
			} `json:"spec"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &page); err != nil || len(page.Items) != 1 || page.Items[0].Spec.Limits.MemoryMax != 1<<30 || page.Items[0].Status.Phase != "ready" {
		t.Fatalf("accounts = %s, %v", out, err)
	}
	acct := page.Items[0].ID
	out, err = cmdIn("", "account", "update", acct, "--note", "Pays yearly")
	if err != nil || !strings.Contains(out, `+ note: "Pays yearly"`) {
		t.Fatalf("update = %s, %v", out, err)
	}
	out, err = cmdIn("", "account", "get", acct)
	if err != nil || !strings.Contains(out, "spec.note:") || !strings.Contains(out, "acme") {
		t.Errorf("get = %s, %v", out, err)
	}
	out, err = cmdIn("", "user", "list", "--scope", "account:"+acct)
	if err != nil || !strings.Contains(out, "acmeowner") || strings.Contains(out, "developer") {
		t.Errorf("the account's users = %s, %v", out, err)
	}
	out, err = cmdIn("", "audit", "list", "--action", "core.account.create")
	if err != nil || !strings.Contains(out, "laptop") {
		t.Errorf("audit = %s, %v", out, err)
	}
	// A token can't change how anyone signs in, so those operations have no
	// commands at all.
	if _, err := cmdIn("", "me", "tokens", "create"); err == nil {
		t.Error("racksail can make API tokens")
	}
	out, err = cmdIn("", "logout")
	if err != nil || !strings.Contains(out, "Removed the profile dev") {
		t.Errorf("logout = %s, %v", out, err)
	}
}
