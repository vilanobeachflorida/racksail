package system

// EffectType groups planned changes to a machine in the change preview.
type EffectType string

// Effect types shown in the change preview.
const (
	EffectDownload    EffectType = "download"
	EffectPackage     EffectType = "package"
	EffectPort        EffectType = "port"
	EffectService     EffectType = "service"
	EffectFile        EffectType = "file"
	EffectUser        EffectType = "user"
	EffectStorage     EffectType = "storage"
	EffectNetwork     EffectType = "network"
	EffectFirewall    EffectType = "firewall"
	EffectKernel      EffectType = "kernel"
	EffectCertificate EffectType = "certificate"
	EffectProcess     EffectType = "process"
)

// Effect is one planned change to a machine, as listed in the preview of a
// change set: a download, a package, a port, a service, a file and so on.
type Effect struct {
	Type EffectType `json:"type" doc:"What kind of change this is"`
	// Action is a verb such as install, remove, open, close, start, stop,
	// write or delete.
	Action string `json:"action" doc:"What happens, for example install, open, start or write"`
	// Target names what changes: a package, "tcp/443", a unit or a path.
	Target string `json:"target" doc:"What changes: a package, a port such as tcp/443, a unit or a path"`
	Detail string `json:"detail,omitempty" doc:"Extra detail such as a version"`
	// Source is where a download comes from.
	Source        string `json:"source,omitempty" doc:"Where a download comes from"`
	DownloadBytes int64  `json:"downloadBytes,omitempty" doc:"Bytes downloaded"`
	DiskBytes     int64  `json:"diskBytes,omitempty" doc:"Estimated disk use in bytes"`
	MemoryBytes   int64  `json:"memoryBytes,omitempty" doc:"Estimated memory use in bytes"`
	// Destructive effects remove data.
	Destructive bool `json:"destructive,omitempty" doc:"Whether it removes data"`
	// Contributor labels effects added by something other than the requested
	// change, such as "hook:backups.second-copy" or "addon:cdn".
	Contributor string `json:"contributor,omitempty" doc:"Who added this effect when it wasn't the requested change itself"`
}
