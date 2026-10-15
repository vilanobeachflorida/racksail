package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// Capability points. A capability is a standard Go interface that providers
// implement, such as an external DNS service or a sign-in method. Providers
// attach to the capability's point with a provide hook; the code that needs
// the capability lists them with Registry.Providers and connects one with
// Open.
//
// The interface of each capability is written by the code that consumes
// it, in internal/hooks/provide_<capability>.go, except where a core
// package already defines it: notification channels are notify.Channel, and
// the identity interfaces of auth.provider and auth.mfa live in
// internal/auth. Modules declare further capabilities as provide.<name>
// points in their manifest's hooks.
const (
	ProvideDNS          Point = "provide.dns"
	ProvideRegistrar    Point = "provide.registrar"
	ProvideBackupTarget Point = "provide.backup-target"
	ProvideNotifier     Point = "provide.notifier"
	ProvideAI           Point = "provide.ai"
	ProvideBilling      Point = "provide.billing"
	ProvideImporter     Point = "provide.importer"
	AuthProvider        Point = "auth.provider"
	AuthMFA             Point = "auth.mfa"
)

// Capabilities maps the capability names add-on manifests use under
// "provides" to their points.
var Capabilities = map[string]Point{
	"dns":           ProvideDNS,
	"registrar":     ProvideRegistrar,
	"backup-target": ProvideBackupTarget,
	"notifier":      ProvideNotifier,
	"ai":            ProvideAI,
	"billing":       ProvideBilling,
	"importer":      ProvideImporter,
	"auth-provider": AuthProvider,
	"mfa":           AuthMFA,
}

// IsCapability reports whether providers register on p.
func IsCapability(p Point) bool {
	return strings.HasPrefix(string(p), "provide.") || p == AuthProvider || p == AuthMFA
}

// ErrUnknownProvider is returned by Open for a provider that isn't
// registered, for example because its add-on was removed.
var ErrUnknownProvider = i18n.Define("hooks.unknown_provider", http.StatusNotFound, "There is no {capability} provider called {provider}.")

var providerNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Provider is one implementation of a capability, such as Cloudflare for
// provide.dns or LDAP for auth.provider. Modules register their built-in
// providers from HookProvider; the add-on runtime registers the providers
// add-ons declare when they are installed.
type Provider struct {
	// Name identifies the provider within its capability, such as
	// "cloudflare". It is what saved settings refer to.
	Name string
	// Title and Description are shown when people choose a provider.
	Title       string
	Description string
	// Settings is a zero value of the provider's settings type. Its JSON
	// Schema builds the form for connecting the provider; fields holding
	// secrets are marked sensitive and hold secret references.
	Settings any
	// Open connects the provider with saved settings. The value it returns
	// implements the capability's interface.
	Open func(ctx context.Context, settings json.RawMessage) (any, error)
}

func (p *Provider) check(point Point, owner string) error {
	switch {
	case p == nil:
		return fmt.Errorf("hook %s on %s: a provide hook needs a Provider", owner, point)
	case !IsCapability(point):
		return fmt.Errorf("hook %s: %s isn't a capability point", owner, point)
	case !providerNamePattern.MatchString(p.Name):
		return fmt.Errorf("hook %s on %s: provider name %q must be lowercase letters, digits and hyphens", owner, point, p.Name)
	case strings.TrimSpace(p.Title) == "" || p.Open == nil:
		return fmt.Errorf("hook %s on %s: provider %s needs a title and an Open function", owner, point, p.Name)
	}
	return nil
}

// Providers lists the providers registered on a capability point, in order
// of priority and then owner.
func (r *Registry) Providers(p Point) []Registration {
	return r.For(p, Provide)
}

// Provider returns the provider with the given name on a capability point.
func (r *Registry) Provider(p Point, name string) (Registration, bool) {
	for _, reg := range r.Providers(p) {
		if reg.Provider.Name == name {
			return reg, true
		}
	}
	return Registration{}, false
}

// Open connects the provider called name on a capability point and returns
// it as the capability's interface T, for example a notification channel an
// add-on provides:
//
//	ch, err := hooks.Open[notify.Channel](ctx, svc.Hooks.Registry, hooks.ProvideNotifier, "gotify", settings)
func Open[T any](ctx context.Context, r *Registry, p Point, name string, settings json.RawMessage) (T, error) {
	var zero T
	reg, ok := r.Provider(p, name)
	if !ok {
		return zero, ErrUnknownProvider.New("capability", string(p), "provider", name)
	}
	v, err := reg.Provider.Open(ctx, settings)
	if err != nil {
		return zero, err
	}
	impl, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("hooks: provider %s on %s, from %s, returned %T, which isn't a %s", name, p, reg.Owner, v, reflect.TypeFor[T]())
	}
	return impl, nil
}

// Extend points. Add-ons extend the panel declaratively: entries in the UI,
// and API routes they serve themselves. Modules don't use extend hooks;
// they add API operations through modules.APIProvider and screens through
// their entry in ui/src/modules.
const (
	ExtendNav      Point = "ui.nav"
	ExtendTab      Point = "ui.tab"
	ExtendCard     Point = "ui.card"
	ExtendCommand  Point = "ui.command"
	ExtendSettings Point = "ui.settings"
	ExtendColumn   Point = "ui.column"
	ExtendRoutes   Point = "api.routes"
)

// extendPoints maps the UI extension types of add-on manifests to their
// points.
var extendPoints = map[string]Point{
	"nav": ExtendNav, "tab": ExtendTab, "card": ExtendCard, "command": ExtendCommand,
	"settings": ExtendSettings, "column": ExtendColumn,
}

// ExtendPoint returns the point of a UI extension type, such as "tab".
func ExtendPoint(uiType string) (Point, bool) {
	p, ok := extendPoints[uiType]
	return p, ok
}

// Extension is what an extend hook adds: one declarative UI entry at a ui.*
// point, or the API routes an add-on serves at api.routes.
type Extension struct {
	// UI is the entry; its Type matches the point.
	UI *UIExtension
	// Routes are the routes the add-on serves under /api/v1/addons/<id>/,
	// and Handler serves them, usually by forwarding to the add-on's socket.
	Routes  []AddonRoute
	Handler http.Handler
}

func (x *Extension) check(point Point, owner string) error {
	if x == nil {
		return fmt.Errorf("hook %s on %s: an extend hook needs an Extension", owner, point)
	}
	if !strings.HasPrefix(owner, "addon:") {
		return fmt.Errorf("hook %s on %s: only add-ons use extend hooks; modules add API operations and screens directly", owner, point)
	}
	if point == ExtendRoutes {
		if len(x.Routes) == 0 || x.Handler == nil || x.UI != nil {
			return fmt.Errorf("hook %s on %s: routes need a handler and nothing else", owner, point)
		}
		return nil
	}
	if !slices.Contains(extendPointList(), point) || x.UI == nil || len(x.Routes) > 0 || x.Handler != nil {
		return fmt.Errorf("hook %s on %s: extend hooks add one UI entry at a ui.* point, or routes at %s", owner, point, ExtendRoutes)
	}
	if want, ok := ExtendPoint(x.UI.Type); !ok || want != point {
		return fmt.Errorf("hook %s on %s: a %q entry belongs on %s", owner, point, x.UI.Type, want)
	}
	if strings.TrimSpace(x.UI.Label) == "" {
		return fmt.Errorf("hook %s on %s: UI entries need a label", owner, point)
	}
	return nil
}

func extendPointList() []Point {
	out := make([]Point, 0, len(extendPoints))
	for _, p := range extendPoints {
		out = append(out, p)
	}
	return out
}

// Extensions lists what add-ons added at an extend point, in order.
func (r *Registry) Extensions(p Point) []Registration {
	return r.For(p, Extend)
}
