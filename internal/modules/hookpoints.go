package modules

import (
	"fmt"

	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// DeclareHookPoints declares every hook point the panel has: those of
// every operation and stage of every kind, each module's lifecycle points,
// and the points modules' manifests add, such as system.caddy.config. The
// panel builds its hook registry with it, and the test that every
// operation has its hook points checks the same registry.
func DeclareHookPoints(reg *hooks.Registry, mods *Registry, kinds *store.KindRegistry) error {
	for _, k := range kinds.All() {
		reg.DeclareKind(k)
	}
	for _, m := range mods.All() {
		man := m.Manifest()
		for _, p := range hooks.ModulePoints(man.ID) {
			reg.Declare(p, man.Name+" lifecycle.")
		}
		for _, h := range man.Hooks {
			p := hooks.Point(h.Point)
			if hooks.IsPattern(p) {
				return fmt.Errorf("modules: %s declares the hook point %s, which has wildcards", man.ID, p)
			}
			if _, ok := reg.ConfigService(p); ok {
				continue
			}
			reg.Declare(p, h.Description)
		}
	}
	return nil
}
