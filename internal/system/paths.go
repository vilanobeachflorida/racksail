package system

import "path"

// Standard locations on a Racksail server.
const (
	ConfigDir    = "/etc/racksail"
	ConfigFile   = "/etc/racksail/racksail.toml"
	GeneratedDir = "/etc/racksail/generated"
	CertsDir     = "/etc/racksail/certs"
	StateDir     = "/var/lib/racksail"
	// AgentStateDir is the agent's own state: what it applied, what it is
	// about to roll back and the certificate it joined a cluster with. Only
	// root reads it.
	AgentStateDir = "/var/lib/racksail-agent"
	// AgentClusterFile holds the hub and certificate of a joined server.
	AgentClusterFile = "/var/lib/racksail-agent/cluster.json"
	// AgentLogDir holds the agent's audit trail. Only root reads it, and no
	// operation reaches it, so the panel can't change the record of what it
	// asked for.
	AgentLogDir = "/var/log/racksail-agent"
	// AgentAuditLog is the agent's audit trail, rotated to .1 when large.
	AgentAuditLog = "/var/log/racksail-agent/audit.jsonl"
	// EdgeStateDir holds the edge's certificates and ACME accounts.
	EdgeStateDir = "/var/lib/racksail-edge"
	// CredstoreDir is where systemd looks for encrypted credentials that
	// services import.
	CredstoreDir = "/etc/credstore.encrypted"
	AccountsDir  = "/srv/racksail/accounts"
	RuntimesDir  = "/opt/racksail/runtimes"
	ModulesDir   = "/opt/racksail/modules"
	RunDir       = "/run/racksail"
	AgentSocket  = "/run/racksail/agent.sock"
	EdgeSocket   = "/run/racksail/edge.sock"
	// PanelSocket is the panel's edge-facing endpoint. The edge proxies the
	// panel's hostname to it, and requests that arrive on it are the only ones
	// whose forwarded client address the panel believes. It lives under
	// /run/racksail, which account sandboxes can't reach, so a site can't use
	// it to forge its apparent address. The panel keeps its own TCP port too.
	PanelSocket  = "/run/racksail/panel.sock"
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
// systemd places a slice under the slice its name extends, so the name puts
// every account inside racksail-accounts.slice.
func AccountSlice(slug string) string { return "racksail-accounts-" + slug + ".slice" }

// AccountDir returns the data directory of the account with the given slug.
func AccountDir(slug string) string { return path.Join(AccountsDir, slug) }

// AccountSubIDs returns the subordinate ID range for rootless containers of
// the account with the given UID.
func AccountSubIDs(uid int) IDRange {
	return IDRange{Start: SubIDStart + (uid-AccountUIDMin)*SubIDCount, Count: SubIDCount}
}
