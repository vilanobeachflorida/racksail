package hosting

import (
	_ "embed"

	"github.com/vilanobeachflorida/racksail/internal/modules"
)

//go:embed manifest.yaml
var manifest []byte

// Module is the hosting business module.
type Module struct {
	modules.Base
}

func init() {
	modules.Register(&Module{Base: modules.NewBase(manifest)})
}
