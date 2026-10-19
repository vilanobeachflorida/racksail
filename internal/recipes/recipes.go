package recipes

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/spec"
)

// ItemKind is what a catalog item is.
type ItemKind string

// Item kinds.
const (
	KindApp          ItemKind = "app"
	KindRuntime      ItemKind = "runtime"
	KindService      ItemKind = "service"
	KindStack        ItemKind = "stack"
	KindDNSTemplate  ItemKind = "dns-template"
	KindAutomation   ItemKind = "automation"
	KindTuningPreset ItemKind = "tuning-preset"
	KindModel        ItemKind = "model"
	KindTheme        ItemKind = "theme"
	KindAddon        ItemKind = "addon"
)

// LicenseClass is shown on every item. Nothing that isn't open source is
// installed by default, and installing it shows a notice first.
type LicenseClass string

// License classes.
const (
	OpenSource  LicenseClass = "open-source"
	FreeToUse   LicenseClass = "free-to-use"
	Proprietary LicenseClass = "proprietary"
)

// Errors.
var (
	ErrMissingInput = i18n.Define("recipes.missing_input", http.StatusUnprocessableEntity, "The recipe needs a value for {input}.")
	ErrBadInput     = i18n.Define("recipes.bad_input", http.StatusUnprocessableEntity, "{input} isn't valid: {reason}.")
	ErrUnknownRef   = i18n.Define("recipes.unknown_reference", http.StatusUnprocessableEntity, "The recipe refers to {ref}, which it doesn't define.")
)

// Recipe is one catalog item.
type Recipe struct {
	Format       int           `yaml:"format" json:"format"`
	Kind         ItemKind      `yaml:"kind" json:"kind"`
	ID           string        `yaml:"id" json:"id"`
	Name         string        `yaml:"name" json:"name"`
	Version      string        `yaml:"version" json:"version"`
	Revision     int           `yaml:"revision,omitempty" json:"revision,omitempty"`
	Summary      string        `yaml:"summary" json:"summary"`
	Description  string        `yaml:"description,omitempty" json:"description,omitempty"`
	License      string        `yaml:"license" json:"license"`
	LicenseClass LicenseClass  `yaml:"licenseClass" json:"licenseClass"`
	Homepage     string        `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	Source       string        `yaml:"source,omitempty" json:"source,omitempty"`
	Icon         string        `yaml:"icon,omitempty" json:"icon,omitempty"`
	Categories   []string      `yaml:"categories,omitempty" json:"categories,omitempty"`
	Maintainers  []Maintainer  `yaml:"maintainers" json:"maintainers"`
	Requires     Requirements  `yaml:"requires,omitempty" json:"requires,omitzero"`
	Inputs       []Input       `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Resources    []Resource    `yaml:"resources,omitempty" json:"resources,omitempty"`
	Downloads    []Download    `yaml:"downloads,omitempty" json:"downloads,omitempty"`
	Health       []HealthCheck `yaml:"health,omitempty" json:"health,omitempty"`
	Model        *Model        `yaml:"model,omitempty" json:"model,omitempty"`
	Upgrade      Upgrade       `yaml:"upgrade,omitempty" json:"upgrade,omitzero"`
	Uninstall    Uninstall     `yaml:"uninstall,omitempty" json:"uninstall,omitzero"`
	Tests        Tests         `yaml:"tests,omitempty" json:"tests,omitzero"`
}

// Maintainer looks after a recipe.
type Maintainer struct {
	Name   string `yaml:"name" json:"name"`
	GitHub string `yaml:"github,omitempty" json:"github,omitempty"`
}

// Requirements must hold before a recipe can be installed.
type Requirements struct {
	Modules  []string `yaml:"modules,omitempty" json:"modules,omitempty" doc:"The modules that must be on"`
	MemoryMB int      `yaml:"memoryMB,omitempty" json:"memoryMB,omitempty" doc:"The least memory the server must have, in MB"`
	DiskMB   int      `yaml:"diskMB,omitempty" json:"diskMB,omitempty" doc:"The least free disk space it must have, in MB"`
	Arch     []string `yaml:"arch,omitempty" json:"arch,omitempty" doc:"The processor architectures it runs on: amd64, arm64 or both"`
	Features []string `yaml:"features,omitempty" json:"features,omitempty" doc:"Hardware it can't run without, such as kvm"`
}

// Input is a value the install wizard asks for.
type Input struct {
	Name        string   `yaml:"name" json:"name"`
	Type        string   `yaml:"type" json:"type"`
	Label       string   `yaml:"label" json:"label"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Default     any      `yaml:"default,omitempty" json:"default,omitempty"`
	Required    bool     `yaml:"required,omitempty" json:"required,omitempty"`
	Options     []Option `yaml:"options,omitempty" json:"options,omitempty"`
	Kind        string   `yaml:"kind,omitempty" json:"kind,omitempty"`
	Pattern     string   `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Min         *float64 `yaml:"min,omitempty" json:"min,omitempty"`
	Max         *float64 `yaml:"max,omitempty" json:"max,omitempty"`
}

// Option is a choice of a select input.
type Option struct {
	Value string `yaml:"value" json:"value"`
	Label string `yaml:"label" json:"label"`
}

// Resource is a resource the recipe creates. String values in Spec may use
// ${inputs.<name>} and ${resources.<name>.id}.
type Resource struct {
	Name      string            `yaml:"name" json:"name"`
	Kind      string            `yaml:"kind" json:"kind"`
	Spec      map[string]any    `yaml:"spec" json:"spec"`
	Labels    map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	DependsOn []string          `yaml:"dependsOn,omitempty" json:"dependsOn,omitempty"`
}

// Download is an upstream file verified against its checksum.
type Download struct {
	URL       string     `yaml:"url" json:"url"`
	SHA256    string     `yaml:"sha256" json:"sha256"`
	Size      int64      `yaml:"size" json:"size"`
	Signature *Signature `yaml:"signature,omitempty" json:"signature,omitempty"`
}

// Signature is a detached signature of a download.
type Signature struct {
	Type string `yaml:"type" json:"type"`
	URL  string `yaml:"url" json:"url"`
	Key  string `yaml:"key,omitempty" json:"key,omitempty"`
}

// HealthResource is the type of health check that waits for a resource the
// recipe creates, named by its name, or for the installed item itself,
// named by the recipe's id, to be ready.
const HealthResource = "resource"

// HealthCheck verifies an installed recipe.
type HealthCheck struct {
	Type           string `yaml:"type" json:"type"`
	Target         string `yaml:"target" json:"target"`
	Expect         string `yaml:"expect,omitempty" json:"expect,omitempty"`
	TimeoutSeconds int    `yaml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
}

// Model describes an assistant model, so the model picker can show it and
// recommend one that fits the server's memory.
type Model struct {
	Family string `yaml:"family" json:"family"`
	// Parameters is the parameter count, such as 8B or 0.6B.
	Parameters    string   `yaml:"parameters" json:"parameters"`
	Quantization  string   `yaml:"quantization,omitempty" json:"quantization,omitempty"`
	Format        string   `yaml:"format" json:"format"`
	ContextLength int      `yaml:"contextLength" json:"contextLength"`
	Size          string   `yaml:"size" json:"size"`
	Capabilities  []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
}

// Upgrade says how new versions are installed.
type Upgrade struct {
	Strategy string `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	Notes    string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Uninstall says what removing the recipe keeps. Data is kept unless the
// recipe or the person uninstalling says otherwise.
type Uninstall struct {
	KeepData *bool  `yaml:"keepData,omitempty" json:"keepData,omitempty"`
	Notes    string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Keeps reports whether uninstalling keeps data by default.
func (u Uninstall) Keeps() bool { return u.KeepData == nil || *u.KeepData }

// Tests describe the recipe's install test in CI.
type Tests struct {
	Inputs         map[string]any `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	TimeoutSeconds int            `yaml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
}

// Parse reads a recipe, checks it against the schema and checks that its
// references and dependencies make sense.
func Parse(data []byte) (*Recipe, error) {
	if err := spec.ValidateYAML(spec.Recipe, data); err != nil {
		return nil, err
	}
	var r Recipe
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("reading the recipe: %w", err)
	}
	if err := r.check(); err != nil {
		return nil, err
	}
	return &r, nil
}

var refPattern = regexp.MustCompile(`\$\{([a-z]+)\.([a-zA-Z0-9]+)(\.id)?\}`)

func (r *Recipe) check() error {
	inputs := map[string]bool{}
	for _, in := range r.Inputs {
		if inputs[in.Name] {
			return fmt.Errorf("recipe %s: input %s is declared twice", r.ID, in.Name)
		}
		inputs[in.Name] = true
		if in.Type == "select" && len(in.Options) == 0 {
			return fmt.Errorf("recipe %s: select input %s has no options", r.ID, in.Name)
		}
	}
	names := map[string]bool{}
	for _, res := range r.Resources {
		if names[res.Name] {
			return fmt.Errorf("recipe %s: resource %s is declared twice", r.ID, res.Name)
		}
		names[res.Name] = true
	}
	for _, res := range r.Resources {
		for _, dep := range res.DependsOn {
			if !names[dep] || dep == res.Name {
				return fmt.Errorf("recipe %s: resource %s depends on unknown resource %s", r.ID, res.Name, dep)
			}
		}
		raw, err := json.Marshal(res.Spec)
		if err != nil {
			return err
		}
		for _, m := range refPattern.FindAllStringSubmatch(string(raw), -1) {
			switch {
			case m[1] == "inputs" && !inputs[m[2]]:
				return ErrUnknownRef.New("ref", m[0])
			case m[1] == "resources" && (!names[m[2]] || m[3] == ""):
				return ErrUnknownRef.New("ref", m[0])
			case m[1] != "inputs" && m[1] != "resources":
				return ErrUnknownRef.New("ref", m[0])
			}
		}
	}
	for i, hc := range r.Health {
		for _, m := range refPattern.FindAllStringSubmatch(hc.Target, -1) {
			if !(m[1] == "inputs" && inputs[m[2]] && m[3] == "") && !(m[1] == "resources" && names[m[2]] && m[3] != "") {
				return ErrUnknownRef.New("ref", m[0])
			}
		}
		if hc.Type == HealthResource && !names[hc.Target] && hc.Target != r.ID {
			return fmt.Errorf("recipe %s: health check %d waits for %s, which is neither one of its resources nor the recipe's id", r.ID, i+1, hc.Target)
		}
	}
	if _, err := r.Order(); err != nil {
		return err
	}
	return nil
}

// Order returns the resources so that each comes after what it depends on.
func (r *Recipe) Order() ([]Resource, error) {
	byName := map[string]Resource{}
	for _, res := range r.Resources {
		byName[res.Name] = res
	}
	var out []Resource
	state := map[string]int{}
	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("recipe %s: resources depend on each other in a cycle through %s", r.ID, name)
		case 2:
			return nil
		}
		state[name] = 1
		res := byName[name]
		deps := slices.Clone(res.DependsOn)
		sort.Strings(deps)
		for _, d := range deps {
			if err := visit(d); err != nil {
				return err
			}
		}
		state[name] = 2
		out = append(out, res)
		return nil
	}
	for _, res := range r.Resources {
		if err := visit(res.Name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Values checks the inputs a person gave against the recipe and fills in
// defaults.
func (r *Recipe) Values(given map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for _, in := range r.Inputs {
		v, ok := given[in.Name]
		if !ok || v == nil || v == "" {
			if in.Default != nil {
				out[in.Name] = in.Default
				continue
			}
			if in.Required {
				return nil, ErrMissingInput.New("input", in.Label)
			}
			continue
		}
		if err := checkValue(in, v); err != nil {
			return nil, ErrBadInput.New("input", in.Label, "reason", err.Error())
		}
		out[in.Name] = v
	}
	for name := range given {
		if !slices.ContainsFunc(r.Inputs, func(in Input) bool { return in.Name == name }) {
			return nil, ErrBadInput.New("input", name, "reason", "the recipe doesn't ask for it")
		}
	}
	return out, nil
}

var (
	domainPattern = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	emailPattern  = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

func checkValue(in Input, v any) error {
	switch in.Type {
	case "boolean":
		if _, ok := v.(bool); !ok {
			return errors.New("expected true or false")
		}
		return nil
	case "integer":
		n, ok := toFloat(v)
		if !ok || n != float64(int64(n)) {
			return errors.New("expected a whole number")
		}
		if (in.Min != nil && n < *in.Min) || (in.Max != nil && n > *in.Max) {
			return errors.New("out of range")
		}
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return errors.New("expected text")
	}
	switch in.Type {
	case "domain":
		if !domainPattern.MatchString(s) {
			return errors.New("expected a domain name such as example.com")
		}
	case "email":
		if !emailPattern.MatchString(s) {
			return errors.New("expected an email address")
		}
	case "select":
		if !slices.ContainsFunc(in.Options, func(o Option) bool { return o.Value == s }) {
			return errors.New("not one of the choices")
		}
	case "resource":
		if !store.ID(s).Valid() {
			return errors.New("expected a resource id")
		}
	}
	if in.Pattern != "" {
		re, err := regexp.Compile("^(?:" + in.Pattern + ")$")
		if err != nil {
			return fmt.Errorf("the recipe's pattern is broken: %w", err)
		}
		if !re.MatchString(s) {
			return errors.New("doesn't match the expected format")
		}
	}
	return nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// Request turns a recipe and checked input values into one change set
// request that creates the recipe's resources in dependency order, so the
// whole install is previewed, applied and undone together. Resource IDs are
// chosen up front so later resources can refer to earlier ones. Values for
// the secret fields of a kind go into the change's Secrets, never into a
// spec, and password and secret inputs may only be used in such fields. The
// caller fills in the actor, DryRun and the idempotency key.
func (r *Recipe) Request(values map[string]any, scope store.Scope, kinds *store.KindRegistry) (changeset.Request, error) {
	ordered, err := r.Order()
	if err != nil {
		return changeset.Request{}, err
	}
	ids := map[string]store.ID{}
	infos := map[string]*store.KindInfo{}
	for _, res := range ordered {
		k, ok := kinds.Get(res.Kind)
		if !ok {
			return changeset.Request{}, i18n.ErrUnknownName.New("what", "kind", "name", res.Kind)
		}
		infos[res.Name] = k.Info()
		ids[res.Name] = store.NewID(k.Info().Prefix)
	}
	secretInputs := map[string]bool{}
	for _, in := range r.Inputs {
		if in.Type == "password" || in.Type == "secret" {
			secretInputs[in.Name] = true
		}
	}
	req := changeset.Request{Title: "Install " + r.Name}
	for _, res := range ordered {
		sensitive := store.SensitiveFields(infos[res.Name])
		if err := checkSecretInputs(res.Spec, "", sensitive, secretInputs); err != nil {
			return changeset.Request{}, fmt.Errorf("recipe %s, resource %s: %w", r.ID, res.Name, err)
		}
		spec, err := substitute(res.Spec, values, ids)
		if err != nil {
			return changeset.Request{}, err
		}
		doc, _ := spec.(map[string]any)
		if doc == nil {
			doc = map[string]any{}
		}
		secretValues, err := takeSecrets(doc, sensitive)
		if err != nil {
			return changeset.Request{}, fmt.Errorf("recipe %s, resource %s: %w", r.ID, res.Name, err)
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return changeset.Request{}, err
		}
		labels := map[string]string{"racksail.com/recipe": r.ID}
		for k, v := range res.Labels {
			labels[k] = v
		}
		req.Changes = append(req.Changes, changeset.Change{
			Op: changeset.OpCreate, Kind: res.Kind, ID: ids[res.Name], Scope: scope, Spec: raw, Labels: labels, Secrets: secretValues,
		})
	}
	return req, nil
}

// checkSecretInputs refuses specs that put a password or secret input
// anywhere but in one of the kind's secret fields, where the value would be
// stored in plain text.
func checkSecretInputs(v any, path string, sensitive []string, secretInputs map[string]bool) error {
	switch x := v.(type) {
	case string:
		for _, m := range refPattern.FindAllStringSubmatch(x, -1) {
			if m[1] == "inputs" && secretInputs[m[2]] && !slices.Contains(sensitive, path) {
				return fmt.Errorf("the secret input %s can only be used in a secret field, not in %s", m[2], path)
			}
		}
	case map[string]any:
		for k, child := range x {
			p := k
			if path != "" {
				p = path + "." + k
			}
			if err := checkSecretInputs(child, p, sensitive, secretInputs); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range x {
			if err := checkSecretInputs(child, path+"[]", sensitive, secretInputs); err != nil {
				return err
			}
		}
	}
	return nil
}

// takeSecrets moves the values of secret fields out of a spec.
func takeSecrets(doc map[string]any, sensitive []string) (map[string]string, error) {
	var out map[string]string
	for _, path := range sensitive {
		parts := strings.Split(path, ".")
		parent := doc
		for _, part := range parts[:len(parts)-1] {
			child, ok := parent[part].(map[string]any)
			if !ok {
				parent = nil
				break
			}
			parent = child
		}
		if parent == nil {
			continue
		}
		last := parts[len(parts)-1]
		v, ok := parent[last]
		if !ok || v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("the secret field %s takes text", path)
		}
		delete(parent, last)
		if out == nil {
			out = map[string]string{}
		}
		out[path] = s
	}
	return out, nil
}

func substitute(v any, values map[string]any, ids map[string]store.ID) (any, error) {
	switch x := v.(type) {
	case string:
		// A string that is exactly one reference keeps the input's type.
		if m := refPattern.FindStringSubmatch(x); m != nil && m[0] == x && m[1] == "inputs" {
			return values[m[2]], nil
		}
		var err error
		out := refPattern.ReplaceAllStringFunc(x, func(ref string) string {
			m := refPattern.FindStringSubmatch(ref)
			if m[1] == "resources" {
				return string(ids[m[2]])
			}
			val, ok := values[m[2]]
			if !ok {
				err = ErrMissingInput.New("input", m[2])
				return ""
			}
			return fmt.Sprint(val)
		})
		return out, err
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			s, err := substitute(val, values, ids)
			if err != nil {
				return nil, err
			}
			out[k] = s
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			s, err := substitute(val, values, ids)
			if err != nil {
				return nil, err
			}
			out[i] = s
		}
		return out, nil
	}
	return v, nil
}

// Index is a catalog's index: the items it offers.
type Index struct {
	Format  int         `json:"format"`
	Catalog string      `json:"catalog"`
	Built   time.Time   `json:"built"`
	Items   []IndexItem `json:"items"`
}

// IndexItem is one item in an index.
type IndexItem struct {
	ID           string       `json:"id" doc:"The item's id in its catalog"`
	Kind         ItemKind     `json:"kind" doc:"What sort of item it is: an app, a runtime, a service, a stack, a DNS template, an automation, a tuning preset, a model, a theme or an add-on"`
	Name         string       `json:"name" doc:"The item's name"`
	Version      string       `json:"version" doc:"The version of the software it installs"`
	Summary      string       `json:"summary" doc:"What it is, in a line"`
	LicenseClass LicenseClass `json:"licenseClass" doc:"Whether the software is open source, free to use or proprietary; anything but open source shows a notice before it is installed"`
	Categories   []string     `json:"categories,omitempty" doc:"The marketplace's categories it is listed under"`
	// Path locates the recipe file relative to the index.
	Path   string `json:"path" doc:"Where its recipe is, relative to the catalog's index"`
	SHA256 string `json:"sha256" doc:"The SHA-256 of its recipe"`
	// Icon locates the item's icon relative to the index.
	Icon string `json:"icon,omitempty" doc:"Where its icon is, relative to the index"`
	// Requires repeats the recipe's requirements, so a fit check needs no
	// other file.
	Requires Requirements `json:"requires,omitzero" doc:"What the server must have before it can be installed"`
	// Files lists every file of the item, the recipe included, so whatever
	// a panel fetches can be checked against the signed index.
	Files []IndexFile `json:"files,omitempty" doc:"Every file of the item, its recipe included, with the checksums the signed index gives them"`
	// Featured, Broken and Unmaintained come from catalog maintenance.
	Featured     bool `json:"featured,omitempty" doc:"Whether the catalog's maintainers feature it"`
	Broken       bool `json:"broken,omitempty" doc:"Whether the catalog's maintainers found it doesn't work at the moment; the marketplace lists it only when asked to"`
	Unmaintained bool `json:"unmaintained,omitempty" doc:"Whether nobody looks after it any more; the marketplace lists it only when asked to"`
}

// IndexFile is one file of a catalog item.
type IndexFile struct {
	// Path locates the file relative to the index.
	Path   string `json:"path" doc:"Where the file is, relative to the index"`
	SHA256 string `json:"sha256" doc:"The file's SHA-256"`
	Size   int64  `json:"size" doc:"The file's size in bytes"`
}

// VerifyIndex checks an index's Ed25519 signature with the key the admin
// trusted when adding the catalog, and parses it.
func VerifyIndex(data, signature []byte, key ed25519.PublicKey) (*Index, error) {
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, data, signature) {
		return nil, errors.New("the catalog index isn't signed by the trusted key")
	}
	var idx Index
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&idx); err != nil {
		return nil, fmt.Errorf("reading the catalog index: %w", err)
	}
	if idx.Format != 1 {
		return nil, fmt.Errorf("catalog index format %d isn't supported", idx.Format)
	}
	for _, it := range idx.Items {
		paths := []string{it.Path}
		if it.Icon != "" {
			paths = append(paths, it.Icon)
		}
		for _, f := range it.Files {
			paths = append(paths, f.Path)
		}
		for _, p := range paths {
			if strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
				return nil, fmt.Errorf("catalog item %s has an unsafe path %q", it.ID, p)
			}
		}
	}
	return &idx, nil
}
