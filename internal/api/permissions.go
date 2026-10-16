package api

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// The permission matrix. Every operation needs a permission the central
// authorizer checks, or is listed here with the reason it doesn't: public
// operations work without signing in, and signed-in ones act only on the
// caller's own records, describe the API, or show only what the caller may
// see, deciding that themselves. CheckMatrix holds every operation to
// that, and a test runs it on the whole binary, modules included.

// PublicOperations work without signing in.
var PublicOperations = map[string]string{
	"getHealth":              "load balancers and uptime monitors check it",
	"getVersion":             "tools check which version answers",
	"getSignInOptions":       "the sign-in screen shows them",
	"getSetupStatus":         "the panel says whether it waits for its first admin",
	"setUpFirstAdmin":        "the one-time setup code is the credential",
	"signIn":                 "signing in is how one gets a session",
	"signInSecondStep":       "the first step's challenge is the credential",
	"beginSecondStepPasskey": "the first step's challenge is the credential",
	"beginSecondStepMfa":     "the first step's challenge is the credential",
	"beginPasskeySignIn":     "signing in with a passkey starts here",
	"finishPasskeySignIn":    "the passkey's answer is the credential",
	"startProviderSignIn":    "signing in through an outside provider starts here",
	"finishProviderSignIn":   "the provider's answer is the credential",
	"lookUpSignInLink":       "the sign-in link is the credential",
	"useSignInLink":          "the sign-in link is the credential",
	"signOut":                "an expired session's cookie can always be cleared",
}

// SignedInOperations need a signed-in caller and no permission of their own.
var SignedInOperations = map[string]string{
	"listOperations":            "describes the API",
	"listKinds":                 "describes the API",
	"getKind":                   "describes the API",
	"listEventTypes":            "describes the API",
	"listRoles":                 "describes the roles and the permission matrix",
	"streamResources":           "shows only resources the caller may read",
	"getResource":               "authorizes the resource's own read permission",
	"listChangeSets":            "shows the caller's own change sets and those whose resources they may read",
	"getChangeSet":              "shows the caller's own change sets and those whose resources they may read",
	"submitChangeSet":           "the change pipeline authorizes every change",
	"applyChangeSet":            "the change pipeline authorizes every change again",
	"undoChangeSet":             "the change pipeline authorizes the undo's changes",
	"listJobs":                  "shows the caller's own jobs, and others' with core.job.read",
	"getJob":                    "shows the caller's own jobs, and others' with core.job.read",
	"getJobLogs":                "shows the caller's own jobs, and others' with core.job.read",
	"streamJob":                 "shows the caller's own jobs, and others' with core.job.read",
	"cancelJob":                 "cancels the caller's own jobs, and others' with core.job.cancel",
	"retryJob":                  "retries the caller's own jobs, and others' with core.job.retry",
	"getSession":                "the caller's own session",
	"saveSessionPreferences":    "the caller's own preferences",
	"stepUp":                    "the caller confirms it's them",
	"beginStepUpPasskey":        "the caller confirms it's them",
	"stopImpersonating":         "the caller's own session",
	"getMe":                     "the caller's own user",
	"updateMe":                  "the caller's own user",
	"changeMyPassword":          "the caller's own password",
	"beginTotp":                 "the caller's own second steps",
	"confirmTotp":               "the caller's own second steps",
	"removeTotp":                "the caller's own second steps",
	"newRecoveryCodes":          "the caller's own second steps",
	"listMyPasskeys":            "the caller's own passkeys",
	"beginPasskeyRegistration":  "the caller's own passkeys",
	"finishPasskeyRegistration": "the caller's own passkeys",
	"renamePasskey":             "the caller's own passkeys",
	"removePasskey":             "the caller's own passkeys",
	"listMySessions":            "the caller's own sessions",
	"endMySession":              "the caller's own sessions",
	"endMyOtherSessions":        "the caller's own sessions",
	"listMyDevices":             "the caller's own trusted browsers",
	"forgetMyDevice":            "the caller's own trusted browsers",
	"forgetMyDevices":           "the caller's own trusted browsers",
	"listMyTokens":              "the caller's own API tokens",
	"createMyToken":             "a token never gets more than its user's own grants",
	"revokeMyToken":             "the caller's own API tokens",
	"listMySSHKeys":             "the caller's own SSH keys",
	"addMySSHKey":               "the caller's own SSH keys",
	"removeMySSHKey":            "the caller's own SSH keys",
	"listMyLinks":               "the caller's own outside sign-ins",
	"unlinkMyIdentity":          "the caller's own outside sign-ins",
	"listMyMfa":                 "the caller's own second steps",
	"beginMfa":                  "the caller's own second steps",
	"finishMfa":                 "the caller's own second steps",
	"disableMfa":                "the caller's own second steps",
	"getMyAlerts":               "the caller's own security alerts",
	"setMyAlerts":               "the caller's own security alerts",
	"testMyAlerts":              "the caller's own security alerts",
}

func init() {
	for _, p := range []auth.PermissionInfo{
		{Name: PermChangeSetRead, Description: "See every change set, not only your own and those of resources you may read."},
		{Name: PermJobRead, Description: "See every job and its log, not only your own."},
		{Name: PermJobCancel, Description: "Cancel anyone's jobs."},
		{Name: PermJobRetry, Description: "Retry anyone's jobs."},
		{Name: PermEventRead, Description: "See the event timeline where your grants reach."},
		{Name: PermAuditRead, Description: "Read and export the audit log where your grants reach."},
		{Name: PermMetricsRead, Description: "Read the panel's metrics."},
	} {
		auth.RegisterPermission(p.Name, p.Description)
	}
}

// CheckMatrix reports the operations the permission matrix doesn't cover:
// public ones not listed as public, ones that need a permission nothing
// defines, and signed-in ones that need no permission without being listed.
// It also reports listed operations that don't match what they are.
func CheckMatrix(ops []Operation, kinds []store.Kind) []string {
	var problems []string
	for _, op := range ops {
		_, public := PublicOperations[op.ID]
		_, signedIn := SignedInOperations[op.ID]
		switch {
		case op.Public && !public:
			problems = append(problems, fmt.Sprintf("%s works without signing in but isn't in PublicOperations", op.ID))
		case !op.Public && public:
			problems = append(problems, fmt.Sprintf("%s is in PublicOperations but needs a sign-in", op.ID))
		case op.Public:
		case op.Permission == "" && !signedIn:
			problems = append(problems, fmt.Sprintf("%s needs no permission; give it one, or list it in SignedInOperations with the reason", op.ID))
		case op.Permission != "" && signedIn:
			problems = append(problems, fmt.Sprintf("%s needs %s but is in SignedInOperations", op.ID, op.Permission))
		case op.Permission != "" && !auth.Known(auth.Permission(op.Permission), kinds):
			problems = append(problems, fmt.Sprintf("%s needs %s, which no kind defines and nothing registered", op.ID, op.Permission))
		}
	}
	return problems
}

// PermissionMatrix lists every permission, of kinds, of the core and of the
// API's operations, with how far each role reaches with it.
func (s *Server) PermissionMatrix() []MatrixEntry {
	infos := auth.Permissions(s.kinds.All())
	described := map[auth.Permission]string{}
	var perms []auth.Permission
	for _, p := range infos {
		described[p.Name] = p.Description
		perms = append(perms, p.Name)
	}
	for _, op := range s.Catalog() {
		p := auth.Permission(op.Permission)
		if p != "" && described[p] == "" {
			described[p] = op.Summary + "."
			perms = append(perms, p)
		}
	}
	rows := auth.MatrixFor(perms)
	out := make([]MatrixEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, MatrixEntry{Permission: r.Permission, Description: described[r.Permission], Roles: r.Roles})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Permission < out[j].Permission })
	return out
}

var typeOfOpened = reflect.TypeFor[OpenedAccount]()
