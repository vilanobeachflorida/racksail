package hooks_test

import (
	"slices"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/modules/all"

	// The core's operations beside what they serve are checked too.
	_ "github.com/vilanobeachflorida/racksail/internal/hooks/hooksapi"
	_ "github.com/vilanobeachflorida/racksail/internal/modules/modulesapi"
	_ "github.com/vilanobeachflorida/racksail/internal/notify/notifyapi"
	_ "github.com/vilanobeachflorida/racksail/internal/recipes/recipesapi"
)

// outsidePipeline are the mutating operations that change nothing a hook
// point is about: they control the pipeline itself, or keep what isn't a
// resource. Every other operation that changes something either is an
// operation of a kind, whose hook points exist, or takes dryRun and goes
// through the change pipeline, whose changes reach their hook points.
var outsidePipeline = map[string]string{
	"applyChangeSet":       "applies a preview by planning its request again, which runs the hooks of every change",
	"cancelJob":            "stops a job; what the job was doing ends where it stopped",
	"retryJob":             "carries on a job that failed, whose changes went through their hooks when they were planned",
	"enableHook":           "switches a hook back on in the hook pipeline itself; the audit log records it",
	"enterSafeMode":        "takes add-ons out of the hook pipeline, which can't wait for their hooks; the audit log records it",
	"leaveSafeMode":        "lets add-ons back into the hook pipeline; the audit log records it",
	"sendTestNotification": "sends the signed-in person a message and changes nothing",
	"dismissInboxItem":     "hides an item from the signed-in person's own inbox; nothing Racksail manages changes",
	"restoreInboxItem":     "shows a dismissed item in the signed-in person's own inbox again; nothing Racksail manages changes",

	// How people sign in is kept apart from their users, in the identity
	// service's own tables, and never goes through change sets; the audit
	// log records every change of it.
	"signIn":                    "starts a session, which login policies at auth.login.validate can refuse",
	"signInSecondStep":          "finishes signing in with a second step, which login policies can refuse",
	"finishPasskeySignIn":       "starts a session with a passkey, which login policies can refuse",
	"useSignInLink":             "sets the password of the link's user and signs them in, which login policies can refuse",
	"signOut":                   "ends the caller's own session",
	"setUpFirstAdmin":           "adds the first admin through the change pipeline as the panel itself, which runs the user's hooks, after keeping the password",
	"stepUp":                    "confirms it's the caller for the minutes the sign-in policy says, which their session remembers",
	"stopImpersonating":         "ends viewing the panel as someone else, back to the caller's own session",
	"saveSessionPreferences":    "keeps the caller's display preferences, which aren't part of their user's settings",
	"changeMyPassword":          "changes the caller's password, a credential",
	"beginTotp":                 "starts setting up the caller's authenticator app, a credential",
	"confirmTotp":               "finishes setting up the caller's authenticator app, a credential",
	"removeTotp":                "removes the caller's authenticator app, a credential",
	"newRecoveryCodes":          "replaces the caller's recovery codes, credentials",
	"beginPasskeyRegistration":  "starts adding a passkey of the caller's, a credential",
	"finishPasskeyRegistration": "adds a passkey of the caller's, a credential",
	"renamePasskey":             "renames a passkey of the caller's",
	"removePasskey":             "removes a passkey of the caller's, a credential",
	"beginMfa":                  "starts setting up a second step an add-on provides, a credential",
	"finishMfa":                 "finishes setting up a second step an add-on provides, a credential",
	"disableMfa":                "removes a second step an add-on provides, a credential",
	"endMySession":              "ends one of the caller's own sessions",
	"endMyOtherSessions":        "ends the caller's other sessions",
	"forgetMyDevice":            "forgets a browser the caller trusted",
	"forgetMyDevices":           "forgets every browser the caller trusted",
	"createMyToken":             "makes an API token for the caller, a credential",
	"revokeMyToken":             "revokes one of the caller's API tokens",
	"startLinkingMyIdentity":    "starts linking the caller to an outside identity",
	"linkMyIdentity":            "links the caller to their account at an outside provider, which they can then sign in with",
	"unlinkMyIdentity":          "unlinks an outside identity from the caller",
	"setMyAlerts":               "says where the caller's security alerts go, kept with their credentials",
	"testMyAlerts":              "sends the caller a test alert and changes nothing",
	"setUserPassword":           "sets someone's password, or asks them to choose one: their credentials, not their user's settings",
	"resetUser":                 "helps someone back in as the admin chooses: a sign-in link, or their second steps, sessions, tokens or lockout taken away",
	"issueSignInLink":           "gives someone a one-time link to sign in with",
	"revokeUserToken":           "revokes someone's API token",
	"impersonateUser":           "starts a session viewing the panel as someone, which the audit log records with both people",
}

// Every operation of every kind has its hook points, at every stage, and
// every API operation that changes something reaches them. A kind, action
// or operation added without them fails here.
func TestEveryOperationHasHookPoints(t *testing.T) {
	reg := hooks.NewRegistry()
	if err := modules.DeclareHookPoints(reg, modules.Default(), store.Kinds()); err != nil {
		t.Fatal(err)
	}
	stages := []changeset.Stage{changeset.StageValidate, changeset.StagePlan, changeset.StageApplied}
	kindOps := map[string][]string{}
	for _, k := range store.Kinds().All() {
		info := k.Info()
		ops := []string{"create", "update", "delete"}
		for _, a := range info.Actions {
			ops = append(ops, a.Name)
		}
		kindOps[info.Name] = ops
		for _, op := range ops {
			for _, st := range stages {
				if p := hooks.KindPoint(info.Name, op, st); !reg.Known(p) {
					t.Errorf("%s has no hook point %s", info.Name, p)
				}
			}
		}
	}
	for _, id := range all.IDs {
		for _, p := range hooks.ModulePoints(id) {
			if !reg.Known(p) {
				t.Errorf("module %s has no hook point %s", id, p)
			}
		}
	}
	for p := range hooks.StaticPoints {
		if !reg.Known(p) {
			t.Errorf("%s isn't declared", p)
		}
	}

	srv := api.New(api.Options{Modules: modules.Default(), Services: &modules.Services{}})
	checked := 0
	for _, op := range srv.Catalog() {
		if !op.Mutating || op.Risk == store.RiskRead || op.Stream {
			continue
		}
		checked++
		switch ops, isKind := kindOps[op.Kind]; {
		case isKind && slices.Contains(ops, op.Operation):
			for _, st := range stages {
				if p := hooks.KindPoint(op.Kind, op.Operation, st); !reg.Known(p) {
					t.Errorf("%s %s (%s) has no hook point %s", op.Method, op.Path, op.ID, p)
				}
			}
		case op.DryRun:
			// It sends its changes through the change pipeline.
		case outsidePipeline[op.ID] != "":
		default:
			t.Errorf("%s %s (%s) changes something without going through the change pipeline or a hook point", op.Method, op.Path, op.ID)
		}
	}
	if checked == 0 {
		t.Error("no operation was checked")
	}
	for id := range outsidePipeline {
		if !slices.ContainsFunc(srv.Catalog(), func(op api.Operation) bool { return op.ID == id }) {
			t.Errorf("%s is allowed outside the pipeline but doesn't exist any more", id)
		}
	}
}
