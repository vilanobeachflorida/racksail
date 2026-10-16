package modules

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// ModuleSpec is what the admin wants of a module: whether it is on, its
// settings and the optional software it may install.
type ModuleSpec struct {
	Module string `json:"module" pattern:"^[a-z][a-z0-9]*$" doc:"The module's id, such as web" example:"web"`
	// Enabled says whether the module is on. Turning a module on or off is
	// a change like any other, with a preview, and can be undone.
	Enabled bool `json:"enabled" doc:"Whether the module is on"`
	// Settings are checked against the settings schema of the module's
	// manifest.
	Settings map[string]any `json:"settings,omitempty" doc:"The module's settings, as its manifest's settings schema describes them"`
	// OptIn lists the upstream software marked opt-in in the manifest that
	// the admin allows the module to install, such as ZFS.
	OptIn []string `json:"optIn,omitempty" doc:"Optional software the module may install, by its id in the manifest"`
	// Removed says the module was removed: it is off and its software is
	// uninstalled. Turning it on again clears it, and its software is
	// installed again the first time something needs it.
	Removed bool `json:"removed,omitempty" doc:"Whether the module was removed: off, with its software uninstalled until something needs it again"`
}

// Usage counts what uses a module.
type Usage struct {
	// Resources is how many resources of the module's kinds exist.
	Resources int `json:"resources" doc:"How many resources of the module's kinds exist"`
	// Kinds counts them by kind.
	Kinds map[string]int `json:"kinds,omitempty" doc:"How many resources of each kind exist"`
}

// ModuleStatus is what the module manager last saw of a module.
type ModuleStatus struct {
	store.Status
	// State is where the module is in its lifecycle.
	State State `json:"state,omitempty" enum:"off,on,active,stopping,removing" doc:"off, on, active (something uses it), stopping or removing"`
	// Since is when the module last changed state.
	Since *time.Time `json:"since,omitempty" doc:"When the module last changed state"`
	Usage Usage      `json:"usage" doc:"What uses the module"`
	// Installed lists the upstream software found on the server, by its id
	// in the manifest.
	Installed []string `json:"installed,omitempty" doc:"The module's software found installed on the server, by its id in the manifest"`
	// HardwareProblems say why the module can't run on this server.
	HardwareProblems []string `json:"hardwareProblems,omitempty" doc:"Why the module can't run on this server"`
	// Started says the module's work in the panel is running.
	Started bool `json:"started,omitempty" doc:"Whether the module's work in the panel is running"`
	// Uninstalled says the software of a removed module was uninstalled,
	// so the resync doesn't uninstall it again.
	Uninstalled bool `json:"uninstalled,omitempty" doc:"Whether the software of a removed module was uninstalled"`
}

// RemoveInput confirms removing a module.
type RemoveInput struct {
	// Confirm is the module's id or name, typed by the person removing it.
	Confirm string `json:"confirm" minLength:"1" doc:"The module's id or name, typed to confirm removing it"`
	// KeepData removes the software but keeps the module's resources, its
	// data, so they come back when the module is turned on again.
	KeepData bool `json:"keepData,omitempty" doc:"Uninstall the software but keep the module's resources and their data"`
}

// Validate implements the check every action input gets.
func (in RemoveInput) Validate() error {
	if strings.TrimSpace(in.Confirm) == "" {
		return i18n.Invalid(i18n.Field("input.confirm", i18n.ErrRequired))
	}
	return nil
}

// Actions of core.module.
const (
	ActionTurnOn  = "turn_on"
	ActionTurnOff = "turn_off"
	ActionRemove  = "remove"
)

// Kind is core.module: one resource for every module compiled into the
// binary, which the module manager creates when the panel first starts with
// it.
var Kind = store.Define[ModuleSpec, ModuleStatus](store.KindInfo{
	Name: "core.module", Prefix: "mod", Scope: store.ScopeServer,
	Title: "Module", Plural: "modules",
	Description: "A part of Racksail that can be turned on and off, such as web hosting, mail or virtual machines. " +
		"Modules ship in the binary and install nothing until something needs them; turning one on or off is a change with a preview.",
	Actions: []store.Action{
		{
			Name: ActionTurnOn, Title: "Turn on", Summary: "Turn the module on",
			Description: "Makes the module visible and ready. Nothing heavy is installed yet: its software is installed the first time something needs it. " +
				"The modules it needs must be on; POST /api/v1/modules/{id}/actions/turn_on turns them on with it.",
			Risk: store.RiskLow, Reversible: true,
		},
		{
			Name: ActionTurnOff, Title: "Turn off", Summary: "Turn the module off, keeping its data and settings",
			Description: "Stops the module and hides it. Its software, data and settings are kept, so turning it on again brings everything back. " +
				"Modules that need it must be turned off first.",
			Risk: store.RiskMedium, Reversible: true,
		},
		{
			Name: ActionRemove, Title: "Remove", Summary: "Uninstall the module's software, and delete its data unless it is kept",
			Description: "Turns the module off and uninstalls the software it installed. Unless keepData is set, its resources must be deleted in the same change, " +
				"which POST /api/v1/modules/{id}/actions/remove does. Removing needs the module's name typed as confirmation.",
			Risk: store.RiskHigh, Destructive: true, Input: RemoveInput{},
		},
	},
	Indexes: []store.Index{{Name: "module", Path: "module", Unique: true}},
	Risks:   store.Risks{Create: store.RiskLow, Update: store.RiskMedium, Delete: store.RiskHigh},
	UI: store.UIHints{
		Icon: "blocks", TitleField: "module", Hidden: true,
		Columns: []store.Column{{Field: "spec.module"}, {Field: "status.state", Format: "badge"}, {Field: "status.usage.resources"}},
		Fields:  map[string]store.FieldHint{"module": {CreateOnly: true}, "enabled": {Widget: "toggle"}, "optIn": {Advanced: true}},
	},
}).WithValidator(validateModule)

func init() {
	store.RegisterKind(Kind)
}

var moduleIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

func validateModule(ctx context.Context, obj, old *store.Object[ModuleSpec, ModuleStatus]) error {
	if !moduleIDPattern.MatchString(obj.Spec.Module) {
		return i18n.Invalid(i18n.Field("spec.module", i18n.ErrBadValue, "reason", "a module id is lowercase letters and digits"))
	}
	if old != nil && old.Spec.Module != obj.Spec.Module {
		return i18n.Invalid(i18n.Field("spec.module", i18n.ErrImmutable))
	}
	return nil
}

// Errors of module settings.
var ErrSettings = i18n.Define("modules.settings", http.StatusUnprocessableEntity, "The settings of {module} aren't valid: {reason}.")

// ValidateSettings checks settings against the manifest's settings schema.
// A module without a schema takes no settings.
func (m *Manifest) ValidateSettings(settings map[string]any) error {
	if len(m.Settings) == 0 {
		if len(settings) > 0 {
			return ErrSettings.New("module", m.Name, "reason", "the module has no settings")
		}
		return nil
	}
	raw, err := json.Marshal(m.Settings)
	if err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	c := jsonschema.NewCompiler()
	url := "racksail:module-" + m.ID + "-settings.json"
	if err := c.AddResource(url, doc); err != nil {
		return fmt.Errorf("module %s: its settings schema: %w", m.ID, err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		return fmt.Errorf("module %s: its settings schema: %w", m.ID, err)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	inst, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(inst))
	if err != nil {
		return err
	}
	if err := sch.Validate(value); err != nil {
		reason := err.Error()
		if i := strings.LastIndex(reason, "\n"); i >= 0 {
			reason = strings.TrimSpace(reason[i+1:])
		}
		return ErrSettings.New("module", m.Name, "reason", strings.TrimPrefix(reason, "- "))
	}
	return nil
}
