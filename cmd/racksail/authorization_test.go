package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/events/webhook"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/hooks/addons"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/recipes"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// testActors signs a request in as the actor its Authorization header
// names: "Test admin", "Test nobody", or no one without the header.
type testActors map[string]*auth.Actor

func (a testActors) Authenticate(ctx context.Context, r *http.Request) (*auth.Actor, error) {
	name, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Test ")
	if !ok {
		return nil, nil
	}
	actor, ok := a[name]
	if !ok {
		return nil, i18n.ErrUnauthenticated.New()
	}
	return actor.Clone(), nil
}

// sshKey is a public key the table's calls add and find.
const sshKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGFw7LvwMxHf0FpGF5oxM8D5Gd+wMx3sVQyBO9g0R4e7 laptop"

// call is how the test calls one operation: what fills its path, the
// query parameters it needs, and its body, which must be one the operation
// takes, so that its handler runs.
type call struct {
	path  map[string]string
	query url.Values
	body  any
	// open operations need nothing more than a sign-in: they answer with
	// what the caller may see, or about the caller's own things.
	open bool
	// refusals are what an open operation may still answer about the
	// caller's own state or the test's request, such as a ceremony that was
	// never started; none of them is about what the caller may do.
	refusals []string
}

// Every operation the panel serves, the core's, the extension system's,
// the module manager's, the catalog's, notifications' and every module's,
// checks permissions before it reads or does anything for the caller.
// Without signing in nothing protected answers; signed in without any
// grant, every operation answers as the authorizer does, forbidden or not
// found, unless it is one that only needs a sign-in. An operation that
// isn't in the table below, or generated for a kind, fails the test.
func TestEveryPanelOperationIsAuthorized(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := shortTemp(t)
	t.Setenv("XDG_DATA_HOME", dir)
	cfg, err := config.Dev(dir, config.DevAddresses{Panel: "127.0.0.1:0", EdgeHTTP: "127.0.0.1:0", EdgeHTTPS: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	// Every operation is called twice in a row from one address.
	cfg.API.RateLimit, cfg.API.AnonymousRateLimit = 0, 0
	// A kind an add-on adds, which the API serves at paths that name it.
	guard, err := addons.NewKind("site-guard", "Site guard", hooks.AddonKind{
		Name: "guard.rule", Prefix: "grule", Scope: "server", Title: "Guard rule", Plural: "rules", Description: "A rule the site guard enforces.",
		Schema: map[string]any{"type": "object"}, Actions: []hooks.AddonAction{{Name: "purge", Summary: "Purge what the rule caught", Risk: "low"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Kinds().Register(guard); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Kinds().Unregister("guard.rule") })
	d, err := newDevServer(ctx, cfg, dir, false, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer d.close(context.Background())
	// Both are in a session of the panel's, where they confirmed it's them
	// just now, so the operations for people in the panel, and those that
	// ask them to confirm, answer as the authorizer does.
	now := time.Now()
	actors := testActors{
		"admin": {Type: auth.ActorUser, ID: store.NewID(identity.PrefixUser), Name: "admin", Source: auth.SourceUI,
			SessionID: string(store.NewID(identity.PrefixSession)), SteppedUpAt: now, Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}},
		"nobody": {Type: auth.ActorUser, ID: store.NewID(identity.PrefixUser), Name: "nobody", Source: auth.SourceUI,
			SessionID: string(store.NewID(identity.PrefixSession)), SteppedUpAt: now},
	}
	srv, err := d.core.apiServer(actors, false)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	// Things that exist, in an account nobody holds a grant on, so an
	// operation that reads before it authorizes gives itself away.
	account := store.AccountScope(store.NewID("acct"))
	installation := store.NewID(recipes.InstallationKind.Info().Prefix)
	spec, _ := json.Marshal(recipes.InstallationSpec{
		Source: recipes.SourceModule, Module: "web", Item: "secret-shop", ItemKind: recipes.KindApp, Name: "Secret Shop", Version: "9.9.9",
		License: "MIT", LicenseClass: recipes.OpenSource, Recipe: "not a recipe",
	})
	if err := d.core.services.Store.Write(ctx, store.WriteMeta{Actor: "system:tests"}, func(tx store.Tx) error {
		return tx.Create(ctx, &store.Resource{Meta: store.Meta{ID: installation, Kind: recipes.InstallationKind.Name(), Scope: account}, Spec: spec})
	}); err != nil {
		t.Fatal(err)
	}
	catalog := string(store.NewID(recipes.CatalogKind.Info().Prefix))
	user := store.NewID(identity.PrefixUser)
	userSpec, _ := json.Marshal(identity.UserSpec{
		Username: "carol", Name: "Carol", Email: "carol@example.com",
		Grants:  []auth.Grant{{Role: auth.RoleOwner, Scope: account}},
		SSHKeys: []identity.UserSSHKey{{ID: "sshkey_carol", Name: "laptop", PublicKey: sshKey}},
	})
	if err := d.core.services.Store.Write(ctx, store.WriteMeta{Actor: "system:tests"}, func(tx store.Tx) error {
		return tx.Create(ctx, &store.Resource{Meta: store.Meta{ID: user, Kind: identity.UserKind.Name(), Scope: account}, Spec: userSpec})
	}); err != nil {
		t.Fatal(err)
	}
	someone := map[string]string{"id": string(user)}
	provider := map[string]string{"provider": string(store.NewID(identity.ProviderKind.Info().Prefix))}

	ids := map[string]string{"id": string(store.NewID("res"))}
	server := map[string]any{"level": "server"}
	table := map[string]call{
		// The core's own operations.
		"listOperations":  {open: true},
		"listKinds":       {open: true},
		"getKind":         {open: true, path: map[string]string{"name": "core.webhook"}},
		"listEventTypes":  {open: true},
		"listChangeSets":  {open: true},
		"listJobs":        {open: true},
		"streamResources": {open: true},
		"searchResources": {open: true, query: url.Values{"q": {string(installation)}}},
		"listAudit":       {},
		"exportAudit":     {},
		"listEvents":      {},
		"streamEvents":    {},
		"getMetrics":      {},
		"getResource":     {path: map[string]string{"id": string(installation)}},
		"getChangeSet":    {path: map[string]string{"id": string(store.NewID(store.PrefixChangeSet))}},
		"applyChangeSet":  {path: map[string]string{"id": string(store.NewID(store.PrefixChangeSet))}},
		"undoChangeSet":   {path: map[string]string{"id": string(store.NewID(store.PrefixChangeSet))}, body: map[string]any{}},
		"submitChangeSet": {body: map[string]any{"changes": []map[string]any{{
			"op": "create", "kind": "core.webhook", "scope": server, "spec": map[string]any{"url": "https://hooks.example.com/", "events": []string{"*"}},
		}}}},
		"listCoreWebhookDeliveries": {path: map[string]string{"id": string(store.NewID("whk"))}},
		"getCoreWebhookDelivery":    {path: map[string]string{"id": string(store.NewID("whk")), "delivery": string(store.NewID(webhook.DeliveryPrefix))}},
		"getJob":                    {path: map[string]string{"id": string(store.NewID(store.PrefixJob))}},
		"getJobLogs":                {path: map[string]string{"id": string(store.NewID(store.PrefixJob))}},
		"streamJob":                 {path: map[string]string{"id": string(store.NewID(store.PrefixJob))}},
		"cancelJob":                 {path: map[string]string{"id": string(store.NewID(store.PrefixJob))}},
		"retryJob":                  {path: map[string]string{"id": string(store.NewID(store.PrefixJob))}, body: map[string]any{}},
		"listRoles":                 {open: true},

		// The kinds add-ons add, which authorize with the kind's own
		// permissions.
		"listSchemaKindResources":      {path: map[string]string{"name": "guard.rule"}},
		"getSchemaKindResource":        {path: map[string]string{"name": "guard.rule", "id": string(store.NewID("grule"))}},
		"getSchemaKindResourceHistory": {path: map[string]string{"name": "guard.rule", "id": string(store.NewID("grule"))}},
		"createSchemaKindResource":     {path: map[string]string{"name": "guard.rule"}, body: map[string]any{"spec": map[string]any{}, "scope": server}},
		"updateSchemaKindResource":     {path: map[string]string{"name": "guard.rule", "id": string(store.NewID("grule"))}, body: map[string]any{"spec": map[string]any{}}},
		"deleteSchemaKindResource":     {path: map[string]string{"name": "guard.rule", "id": string(store.NewID("grule"))}},
		"runSchemaKindAction":          {path: map[string]string{"name": "guard.rule", "id": string(store.NewID("grule")), "action": "purge"}, body: map[string]any{}},

		// People and access: helping someone back in and viewing the panel
		// as them are for who may manage them, and an account with an owner
		// for who may open accounts.
		"listUserSSHKeys":  {path: someone},
		"listUserSessions": {path: someone},
		"listUserTokens":   {path: someone},
		"revokeUserToken":  {path: map[string]string{"id": string(user), "token": string(store.NewID(identity.PrefixToken))}},
		"issueSignInLink":  {path: someone},
		"setUserPassword":  {path: someone, body: map[string]any{"mustChange": true}},
		"resetUser":        {path: someone, body: map[string]any{"endSessions": true}},
		"impersonateUser":  {path: someone},
		"openCoreAccount":  {body: map[string]any{"slug": "acme", "name": "Acme"}},

		// The caller's own session, user and ways of signing in.
		"getSession":                {open: true},
		"saveSessionPreferences":    {open: true, body: map[string]any{"mode": "dark"}},
		"stepUp":                    {open: true, body: map[string]any{"password": "not their password"}},
		"beginStepUpPasskey":        {open: true},
		"stopImpersonating":         {open: true, refusals: []string{"auth.not_impersonating"}},
		"getMe":                     {open: true},
		"updateMe":                  {open: true, body: map[string]any{"name": "Nobody"}},
		"changeMyPassword":          {open: true, body: map[string]any{"new": "a kettle of tangerines"}},
		"beginTotp":                 {open: true},
		"confirmTotp":               {open: true, body: map[string]any{"code": "123456"}},
		"removeTotp":                {open: true},
		"newRecoveryCodes":          {open: true},
		"listMyPasskeys":            {open: true},
		"beginPasskeyRegistration":  {open: true, refusals: []string{"auth.passkeys_unavailable"}},
		"finishPasskeyRegistration": {open: true, body: map[string]any{"ceremony": "none", "credential": map[string]any{}}, refusals: []string{"auth.challenge_expired"}},
		"renamePasskey":             {open: true, path: map[string]string{"id": string(store.NewID(identity.PrefixPasskey))}, body: map[string]any{"name": "Laptop"}},
		"removePasskey":             {open: true, path: map[string]string{"id": string(store.NewID(identity.PrefixPasskey))}},
		"listMyMfa":                 {open: true},
		"beginMfa":                  {open: true, path: map[string]string{"id": string(store.NewID(identity.ProviderKind.Info().Prefix))}},
		"finishMfa": {open: true, path: map[string]string{"id": string(store.NewID(identity.ProviderKind.Info().Prefix))}, body: map[string]any{"ceremony": "none"},
			refusals: []string{"auth.challenge_expired"}},
		"disableMfa":             {open: true, path: map[string]string{"id": string(store.NewID(identity.ProviderKind.Info().Prefix))}},
		"listMySessions":         {open: true},
		"endMySession":           {open: true, path: map[string]string{"id": string(store.NewID(identity.PrefixSession))}},
		"endMyOtherSessions":     {open: true},
		"listMyDevices":          {open: true},
		"forgetMyDevice":         {open: true, path: map[string]string{"id": string(store.NewID(identity.PrefixDevice))}},
		"forgetMyDevices":        {open: true},
		"listMyTokens":           {open: true},
		"createMyToken":          {open: true, body: map[string]any{"name": "laptop"}},
		"revokeMyToken":          {open: true, path: map[string]string{"id": string(store.NewID(identity.PrefixToken))}},
		"listMySSHKeys":          {open: true},
		"addMySSHKey":            {open: true, body: map[string]any{"name": "laptop", "publicKey": sshKey}},
		"removeMySSHKey":         {open: true, path: map[string]string{"id": "sshkey_carol"}},
		"listMyLinks":            {open: true},
		"linkMyIdentity":         {open: true, path: provider, body: map[string]any{"username": "nobody", "password": "not their password"}},
		"startLinkingMyIdentity": {open: true, path: provider},
		"unlinkMyIdentity":       {open: true, path: provider},
		"getMyAlerts":            {open: true},
		"setMyAlerts":            {open: true, body: map[string]any{}},
		"testMyAlerts":           {open: true},

		// The attention inbox shows what the caller may see, and dismissals
		// are the caller's own.
		"listInbox":        {open: true},
		"streamInbox":      {open: true},
		"dismissInboxItem": {open: true, path: map[string]string{"id": "suggestion:storage"}},
		"restoreInboxItem": {open: true, path: map[string]string{"id": "suggestion:storage"}},

		// Hooks, rules, safe mode and add-ons.
		"listHooks":        {},
		"listHookPoints":   {},
		"enableHook":       {body: map[string]any{"owner": "addon:site-guard", "point": "web.site.create.validate"}},
		"listAddonEntries": {open: true},
		"getSafeMode":      {},
		"enterSafeMode":    {body: map[string]any{"reason": "testing"}},
		"leaveSafeMode":    {},
		"testRule": {body: map[string]any{
			"rule": map[string]any{"name": "Nothing", "type": "validate", "expression": "false"}, "owner": server,
			"operation": "create", "kind": "core.webhook", "scope": server,
		}},

		// The module manager.
		"listModules":        {},
		"getModule":          {path: map[string]string{"id": "web"}},
		"listProfiles":       {},
		"applyProfile":       {path: map[string]string{"profile": "minimal"}},
		"turnOnModule":       {path: map[string]string{"id": "web"}},
		"turnOffModule":      {path: map[string]string{"id": "network"}},
		"removeModule":       {path: map[string]string{"id": "web"}, body: map[string]any{"confirm": "web", "keepData": true}},
		"listModuleSoftware": {path: map[string]string{"id": "mail"}},
		"exportModule":       {path: map[string]string{"id": "web"}},

		// The catalog: its items are for everyone signed in; installing,
		// upgrading and uninstalling are for who may change the account.
		"listCatalogItems":      {open: true},
		"getCatalogItem":        {open: true, path: map[string]string{"catalog": catalog, "item": "secret-shop"}},
		"getCatalogItemIcon":    {open: true, path: map[string]string{"catalog": catalog, "item": "secret-shop"}},
		"installCatalogItem":    {body: map[string]any{"catalog": catalog, "item": "secret-shop", "scope": map[string]any{"level": "account", "id": account.ID}}},
		"upgradeInstallation":   {path: map[string]string{"id": string(installation)}, body: map[string]any{}},
		"uninstallInstallation": {path: map[string]string{"id": string(installation)}, body: map[string]any{}},

		// Notifications: the log and the channels are the server's; each
		// person's preferences are their own.
		"listNotifications":          {},
		"getNotification":            {path: map[string]string{"id": string(store.NewID("ntf"))}},
		"listChannelTypes":           {},
		"getNotificationPreferences": {open: true},
		"setNotificationPreferences": {open: true, body: map[string]any{"email": false}},
		"sendTestNotification":       {open: true},
	}

	do := func(who, method, path string, body any) *http.Response {
		var r io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			r = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, path, r)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if who != "" {
			req.Header.Set("Authorization", "Test "+who)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Result()
	}
	problem := func(resp *http.Response) (string, string) {
		raw, _ := io.ReadAll(resp.Body)
		var p struct {
			Code string `json:"code"`
		}
		json.Unmarshal(raw, &p)
		return p.Code, string(raw)
	}

	seen := map[string]bool{}
	checked := 0
	for _, op := range srv.Catalog() {
		if op.Public {
			continue
		}
		if generatedForKind(op) {
			// Their handlers are the same code for every kind, which the
			// API's own tests call with kinds and bodies of their own;
			// here, what needs no body is called for each kind.
			path := strings.ReplaceAll(op.Path, "{id}", ids["id"])
			if resp := do("", op.Method, path, nil); resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s without signing in = %d", op.ID, resp.StatusCode)
			}
			if op.Operation == "list" || op.Operation == "get" || op.Operation == "history" || op.Operation == "delete" {
				if resp := do("nobody", op.Method, path+"?dryRun=true", nil); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
					code, body := problem(resp)
					t.Errorf("%s without a grant = %d %s %s", op.ID, resp.StatusCode, code, body)
				}
			}
			continue
		}
		c, ok := table[op.ID]
		if !ok {
			t.Errorf("%s %s (%s) isn't in the table of how its authorization is checked; add it", op.Method, op.Path, op.ID)
			continue
		}
		seen[op.ID] = true
		checked++
		path := op.Path
		for name, value := range c.path {
			path = strings.ReplaceAll(path, "{"+name+"}", value)
		}
		if strings.Contains(path, "{") {
			t.Errorf("%s: the table doesn't fill in %s", op.ID, path)
			continue
		}
		params := url.Values{}
		for name, values := range c.query {
			params[name] = values
		}
		if op.DryRun {
			params.Set("dryRun", "true")
		}
		query := ""
		if len(params) > 0 {
			query = "?" + params.Encode()
		}
		if resp := do("", op.Method, path+query, c.body); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without signing in = %d", op.ID, resp.StatusCode)
		}
		if op.Stream && c.open {
			continue
		}
		resp := do("nobody", op.Method, path+query, c.body)
		code, body := problem(resp)
		switch {
		case c.open:
			if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound && !slices.Contains(c.refusals, code) {
				t.Errorf("%s needs only a sign-in, but answered %d %s", op.ID, resp.StatusCode, body)
			}
		case resp.StatusCode < 400:
			t.Errorf("%s succeeded without a grant: %d %s", op.ID, resp.StatusCode, body)
		case code != "core.forbidden" && code != "core.not_found":
			// Anything else means the handler read or did something for
			// the caller before it asked the authorizer.
			t.Errorf("%s answered %d %s without a grant, not as the authorizer would: %s", op.ID, resp.StatusCode, code, body)
		}
	}
	if checked == 0 {
		t.Fatal("no operation was checked")
	}
	for id := range table {
		if !seen[id] {
			t.Errorf("%s is in the table but the panel has no such operation", id)
		}
	}

	// The routes add-ons serve, which the operation catalog doesn't list,
	// answer only those who may use the add-on.
	for _, path := range []string{"/api/v1/addons/site-guard/scans", "/api/v1/addons/site-guard/ui/index.html"} {
		if resp := do("", http.MethodGet, path, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without signing in = %d", path, resp.StatusCode)
		}
		if resp := do("nobody", http.MethodGet, path, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s without a grant = %d", path, resp.StatusCode)
		}
	}

	// The admin reaches what nobody may: the table's calls are real ones.
	if resp := do("admin", http.MethodPost, "/api/v1/catalog/installations/"+string(installation)+"/upgrade?dryRun=true", map[string]any{}); resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		code, body := problem(resp)
		t.Errorf("the admin upgrading the installation = %d %s %s", resp.StatusCode, code, body)
	}
}

// generatedForKind reports whether the API generated an operation for a
// kind: list, create, get, update, delete and history, and one for each of
// its actions, at the kind's own path.
func generatedForKind(op api.Operation) bool {
	k, ok := store.Kinds().Get(op.Kind)
	if !ok || op.Kind == "" {
		return false
	}
	info := k.Info()
	base := api.KindPath(info)
	switch {
	case op.Path == base:
		return (op.Method == http.MethodGet && op.Operation == "list") || (op.Method == http.MethodPost && op.Operation == "create")
	case op.Path == base+"/{id}":
		return slices.Contains([]string{"get", "update", "delete"}, op.Operation)
	case op.Path == base+"/{id}/history":
		return op.Operation == "history"
	case strings.HasPrefix(op.Path, base+"/{id}/actions/"):
		name := strings.TrimPrefix(op.Path, base+"/{id}/actions/")
		return name == op.Operation && slices.ContainsFunc(info.Actions, func(a store.Action) bool { return a.Name == name })
	}
	return false
}
