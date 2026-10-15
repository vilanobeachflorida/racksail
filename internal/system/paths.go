package system

import "path"

// Standard locations on a Racksail server.
const (
	ConfigDir    = "/etc/racksail"
	ConfigFile   = "/etc/racksail/racksail.toml"
	GeneratedDir = "/etc/racksail/generated"
	CertsDir     = "/etc/racksail/certs"
	StateDir     = "/var/lib/racksail"
	AccountsDir  = "/srv/racksail/accounts"
	RuntimesDir  = "/opt/racksail/runtimes"
	ModulesDir   = "/opt/racksail/modules"
	RunDir       = "/run/racksail"
	AgentSocket  = "/run/racksail/agent.sock"
	EdgeSocket   = "/run/racksail/edge.sock"
	DataDisksDir = "/mnt/racksail"
	UnitDir      = "/etc/systemd/system"
	NetworkdDir  = "/etc/systemd/network"
	AptSourceDir = "/etc/apt/sources.list.d"
	AptKeyDir    = "/etc/apt/keyrings"
	AptPrefsDir  = "/etc/apt/preferences.d"
)

// systemd slices.
const (
	PlatformSlice = "racksail.slice"
	AccountsSlice = "racksail-accounts.slice"
)

// Account identity ranges.
const (
	AccountUIDMin = 20000
	AccountUIDMax = 59999
	SubIDStart    = 1_000_000
	SubIDCount    = 65_536
)

// AccountSlice returns the slice of the account with the given slug.
func AccountSlice(slug string) string { return "racksail-acct-" + slug + ".slice" }

// AccountDir returns the data directory of the account with the given slug.
func AccountDir(slug string) string { return path.Join(AccountsDir, slug) }

// AccountSubIDs returns the subordinate ID range for rootless containers of
// the account with the given UID.
func AccountSubIDs(uid int) IDRange {
	return IDRange{Start: SubIDStart + (uid-AccountUIDMin)*SubIDCount, Count: SubIDCount}
}
