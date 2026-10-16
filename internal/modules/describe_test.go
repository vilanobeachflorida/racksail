package modules_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/hooks/addons"
	"github.com/vilanobeachflorida/racksail/internal/hooks/rules"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/notify"
	"github.com/vilanobeachflorida/racksail/internal/recipes"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/updates"
)

// Every field of the settings of the panel's own kinds that the extension
// system, the module manager, catalogs, notifications and updates keep,
// and of their actions' inputs, has a description: the API document, the
// CLI's flags, the forms and the assistant's tools all show it.
func TestSettingsAreDescribed(t *testing.T) {
	kinds := []store.Kind{
		rules.Policy, rules.ResellerPolicy, rules.AccountPolicy, addons.Addon, addons.AccountAddon,
		recipes.CatalogKind, recipes.InstallationKind, recipes.ServerInstallationKind,
		notify.ChannelKind, notify.RelayKind, notify.PreferencesKind, updates.Kind, modules.Kind,
	}
	reg := huma.NewMapRegistry("#/components/schemas/", httpapi.SchemaNamer)
	for _, k := range kinds {
		info := k.Info()
		undescribed(t, reg, info.Name+".spec", reg.Schema(k.SpecType(), true, ""), nil)
		for _, a := range info.Actions {
			if a.Input != nil {
				undescribed(t, reg, info.Name+"."+a.Name+".input", reg.Schema(reflect.TypeOf(a.Input), true, ""), nil)
			}
		}
	}
}

// undescribed reports the properties of a schema, and of the schemas it
// refers to, that have no description.
func undescribed(t *testing.T, reg huma.Registry, at string, s *huma.Schema, seen []string) {
	t.Helper()
	if s == nil {
		return
	}
	if s.Ref != "" {
		if slices.Contains(seen, s.Ref) {
			return
		}
		undescribed(t, reg, at, reg.SchemaFromRef(s.Ref), append(seen, s.Ref))
		return
	}
	for name, p := range s.Properties {
		if strings.TrimSpace(p.Description) == "" {
			t.Errorf("%s.%s has no description", at, name)
		}
		undescribed(t, reg, at+"."+name, p, seen)
	}
	undescribed(t, reg, at+"[]", s.Items, seen)
	if extra, ok := s.AdditionalProperties.(*huma.Schema); ok {
		undescribed(t, reg, at+"{}", extra, seen)
	}
}
