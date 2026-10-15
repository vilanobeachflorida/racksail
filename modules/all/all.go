// Package all imports every bundled module, so a binary that imports it
// has them all registered. Both this list and ui/src/modules/registry.ts
// name every module from the start, so work on one module never touches
// another's files.
package all

import (
	_ "github.com/vilanobeachflorida/racksail/modules/ai"
	_ "github.com/vilanobeachflorida/racksail/modules/automations"
	_ "github.com/vilanobeachflorida/racksail/modules/backups"
	_ "github.com/vilanobeachflorida/racksail/modules/builder"
	_ "github.com/vilanobeachflorida/racksail/modules/cluster"
	_ "github.com/vilanobeachflorida/racksail/modules/containers"
	_ "github.com/vilanobeachflorida/racksail/modules/databases"
	_ "github.com/vilanobeachflorida/racksail/modules/dns"
	_ "github.com/vilanobeachflorida/racksail/modules/files"
	_ "github.com/vilanobeachflorida/racksail/modules/home"
	_ "github.com/vilanobeachflorida/racksail/modules/hosting"
	_ "github.com/vilanobeachflorida/racksail/modules/mail"
	_ "github.com/vilanobeachflorida/racksail/modules/marketplace"
	_ "github.com/vilanobeachflorida/racksail/modules/migration"
	_ "github.com/vilanobeachflorida/racksail/modules/monitoring"
	_ "github.com/vilanobeachflorida/racksail/modules/network"
	_ "github.com/vilanobeachflorida/racksail/modules/performance"
	_ "github.com/vilanobeachflorida/racksail/modules/security"
	_ "github.com/vilanobeachflorida/racksail/modules/shares"
	_ "github.com/vilanobeachflorida/racksail/modules/storage"
	_ "github.com/vilanobeachflorida/racksail/modules/vms"
	_ "github.com/vilanobeachflorida/racksail/modules/web"
)

// IDs lists the bundled modules in the order modules.md describes them.
var IDs = []string{
	"network",
	"web",
	"hosting",
	"databases",
	"dns",
	"mail",
	"containers",
	"vms",
	"storage",
	"shares",
	"home",
	"marketplace",
	"backups",
	"monitoring",
	"files",
	"performance",
	"builder",
	"ai",
	"automations",
	"migration",
	"cluster",
	"security",
}
