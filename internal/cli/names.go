package cli

import (
	"strings"
	"unicode"

	"github.com/vilanobeachflorida/racksail/internal/api"
)

// coreCommands names the commands of the core's operations that aren't
// about a kind. Operations on kinds are named after the kind and the
// operation, such as "account list" or "web site create"; operations a
// module adds otherwise are named after the module and the operation's ID.
var coreCommands = map[string]string{
	"listChangeSets":  "changeset list",
	"getChangeSet":    "changeset get",
	"submitChangeSet": "changeset submit",
	"applyChangeSet":  "changeset apply",
	"undoChangeSet":   "changeset undo",

	"listJobs":   "job list",
	"getJob":     "job get",
	"getJobLogs": "job logs",
	"streamJob":  "job watch",
	"cancelJob":  "job cancel",
	"retryJob":   "job retry",

	"listEvents":     "event list",
	"listEventTypes": "event types",
	"streamEvents":   "event watch",
	"listAudit":      "audit list",
	"exportAudit":    "audit export",

	"getHealth":       "health",
	"getVersion":      "server-version",
	"getMetrics":      "metrics",
	"listOperations":  "operation list",
	"listKinds":       "kind list",
	"getKind":         "kind get",
	"getResource":     "resource get",
	"streamResources": "resource watch",

	"resetUser":        "user reset",
	"setUserPassword":  "user password",
	"issueSignInLink":  "user link",
	"listUserSessions": "user sessions",
	"listUserSSHKeys":  "user ssh-keys",
	"listUserTokens":   "user tokens",
	"revokeUserToken":  "user revoke-token",
	"listRoles":        "role list",

	"getMe":          "me show",
	"listMySessions": "me sessions",
	"listMyDevices":  "me devices",
	"listMyTokens":   "me tokens",
	"revokeMyToken":  "me revoke-token",
	"listMySSHKeys":  "me ssh-keys",
	"listMyPasskeys": "me passkeys",
	"listMyLinks":    "me links",
	"listMyMfa":      "me mfa",
	"getMyAlerts":    "me alerts",

	"openCoreAccount": "account open",
}

// handWritten are operations commands of this package cover by hand.
var handWritten = map[string]string{
	"getSession": "whoami",
}

// groupTitles are the one-line help of the core's command groups.
var groupTitles = map[string]string{
	"changeset": "Preview, apply and undo change sets",
	"job":       "Follow, cancel and retry background jobs",
	"event":     "Read and follow the event timeline",
	"audit":     "Read and export the audit log",
	"operation": "List what the API can do",
	"kind":      "Describe the kinds of resources",
	"resource":  "Get any resource by its ID, and follow changes",
	"user":      "Manage users, their grants and their access",
	"role":      "List roles and the permission matrix",
	"me":        "See your own user and how you sign in",
}

// CommandPath is the words of the command for an operation, after
// racksail: "account list", "web site create" or "job watch".
func CommandPath(op *Operation) []string {
	if op.Kind != "" && op.Operation != "" {
		module, name, _ := strings.Cut(op.Kind, ".")
		words := []string{kebab(name)}
		if module != "core" {
			words = append([]string{module}, words...)
		}
		return append(words, kebab(op.Operation))
	}
	if name, ok := coreCommands[op.ID]; ok {
		return strings.Fields(name)
	}
	if name, ok := handWritten[op.ID]; ok {
		return strings.Fields(name)
	}
	rest := strings.TrimPrefix(op.Path, api.Prefix+"/")
	first, _, _ := strings.Cut(rest, "/")
	return []string{kebab(first), kebab(op.ID)}
}

// kebab turns camelCase and snake_case names into kebab-case:
// memoryMax becomes memory-max, signin_policy signin-policy and
// listMySSHKeys list-my-ssh-keys.
func kebab(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case r == '_' || r == ' ' || r == '.':
			b.WriteByte('-')
		case unicode.IsUpper(r):
			prevLower := i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]))
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			prevUpper := i > 0 && unicode.IsUpper(runes[i-1])
			if i > 0 && (prevLower || (prevUpper && nextLower)) {
				b.WriteByte('-')
			}
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return strings.Trim(strings.ReplaceAll(b.String(), "--", "-"), "-")
}

// pluralTitle makes the plural of a kind's title for help text: Account
// becomes Accounts, Sign-in policy Sign-in policies and Mailbox Mailboxes.
func pluralTitle(title string) string {
	lower := strings.ToLower(title)
	switch {
	case strings.HasSuffix(lower, "y") && len(lower) > 1 && !strings.ContainsRune("aeiou", rune(lower[len(lower)-2])):
		return title[:len(title)-1] + "ies"
	case strings.HasSuffix(lower, "s"), strings.HasSuffix(lower, "x"), strings.HasSuffix(lower, "z"),
		strings.HasSuffix(lower, "ch"), strings.HasSuffix(lower, "sh"):
		return title + "es"
	}
	return title + "s"
}

// lowerFirst lowercases the first letter of a title for the middle of a
// sentence, leaving abbreviations such as DNS alone.
func lowerFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 || (len(r) > 1 && unicode.IsUpper(r[1])) {
		return s
	}
	r[0] = unicode.ToLower(r[0])
	return string(r)
}
