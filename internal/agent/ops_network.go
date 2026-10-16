package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// lastRuleset is where the agent keeps the ruleset it applied, so applying
// the same one again changes nothing.
const lastRuleset = system.AgentStateDir + "/firewall.nft"

// ApplyFirewallInput is a complete nftables ruleset for Racksail's tables.
type ApplyFirewallInput struct {
	Ruleset string `json:"ruleset" doc:"nftables ruleset; only racksail* tables"`
}

// Validate implements Input.
func (in ApplyFirewallInput) Validate() error {
	if len(in.Ruleset) > 4<<20 {
		return fmt.Errorf("the ruleset is larger than 4 MiB")
	}
	_, err := system.CheckRuleset([]byte(in.Ruleset))
	return err
}

// ApplyFirewall replaces Racksail's nftables tables atomically after nft has
// checked the new ruleset.
var ApplyFirewall = Register(Def[ApplyFirewallInput, Result]{
	Name:        "ApplyFirewall",
	Summary:     "Apply the firewall ruleset",
	Description: "Checks the ruleset with nftables and then replaces every racksail* table in one transaction.",
	Effects: func(in ApplyFirewallInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFirewall, Action: "apply", Target: "nftables"}}
	},
	Run: func(ctx context.Context, env *Env, in ApplyFirewallInput) (Result, error) {
		fw := env.System.Firewall
		ruleset := []byte(in.Ruleset)
		if err := fw.Check(ctx, ruleset); err != nil {
			return Result{}, err
		}
		last, err := env.System.Files.ReadFile(ctx, lastRuleset, 8<<20)
		if err == nil && bytes.Equal(last, ruleset) {
			if live, err := fw.Ruleset(ctx); err == nil && len(bytes.TrimSpace(live)) > 0 {
				return Unchanged(), nil
			}
		}
		effect := system.Effect{Type: system.EffectFirewall, Action: "apply", Target: "nftables"}
		if env.Check {
			return Changed(effect), nil
		}
		if err := fw.Apply(ctx, ruleset); err != nil {
			return Result{}, err
		}
		if err := ensureStateDir(ctx, env); err != nil {
			return Result{}, err
		}
		return Changed(effect), env.System.Files.WriteFile(ctx, lastRuleset, ruleset, 0o600, system.Owner{})
	},
})

// ensureStateDir creates the agent's state directory, root's alone.
func ensureStateDir(ctx context.Context, env *Env) error {
	f := env.System.Files
	if _, err := f.Stat(ctx, system.AgentStateDir); err == nil {
		return nil
	}
	if err := f.Mkdir(ctx, system.AgentStateDir, 0o700, system.Owner{}); err != nil && !errors.Is(err, system.ErrExists) {
		return err
	}
	return nil
}

// SetElementsInput adds or removes set elements, such as bans.
type SetElementsInput struct {
	Set      system.SetRef       `json:"set"`
	Elements []system.SetElement `json:"elements"`
}

// Validate implements Input.
func (in SetElementsInput) Validate() error {
	if !in.Set.Valid() {
		return fmt.Errorf("set %s %s %s isn't one of Racksail's", in.Set.Family, in.Set.Table, in.Set.Set)
	}
	if len(in.Elements) == 0 || len(in.Elements) > 10000 {
		return fmt.Errorf("give between 1 and 10000 elements")
	}
	for _, e := range in.Elements {
		if _, err := netip.ParseAddr(e.Value); err != nil {
			if _, err := netip.ParsePrefix(e.Value); err != nil {
				return fmt.Errorf("%q isn't an address or prefix", e.Value)
			}
		}
		if e.Timeout < 0 {
			return fmt.Errorf("timeout of %s is negative", e.Value)
		}
	}
	return nil
}

// AddFirewallElements adds addresses to a set without touching the rules;
// brute-force bans use it.
var AddFirewallElements = Register(Def[SetElementsInput, Result]{
	Name:    "AddFirewallElements",
	Summary: "Add addresses to a firewall set",
	Effects: func(in SetElementsInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFirewall, Action: "add-elements", Target: in.Set.Set, Detail: fmt.Sprint(len(in.Elements))}}
	},
	Run: func(ctx context.Context, env *Env, in SetElementsInput) (Result, error) {
		effect := system.Effect{Type: system.EffectFirewall, Action: "add-elements", Target: in.Set.Set, Detail: fmt.Sprint(len(in.Elements))}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Firewall.AddElements(ctx, in.Set, in.Elements...)
	},
})

// DeleteFirewallElements removes addresses from a set.
var DeleteFirewallElements = Register(Def[SetElementsInput, Result]{
	Name:    "DeleteFirewallElements",
	Summary: "Remove addresses from a firewall set",
	Effects: func(in SetElementsInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFirewall, Action: "delete-elements", Target: in.Set.Set, Detail: fmt.Sprint(len(in.Elements))}}
	},
	Run: func(ctx context.Context, env *Env, in SetElementsInput) (Result, error) {
		effect := system.Effect{Type: system.EffectFirewall, Action: "delete-elements", Target: in.Set.Set, Detail: fmt.Sprint(len(in.Elements))}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Firewall.DeleteElements(ctx, in.Set, in.Elements...)
	},
})

// SetRefInput names a firewall set.
type SetRefInput struct {
	Set system.SetRef `json:"set"`
}

// Validate implements Input.
func (in SetRefInput) Validate() error {
	if !in.Set.Valid() {
		return fmt.Errorf("set %s %s %s isn't one of Racksail's", in.Set.Family, in.Set.Table, in.Set.Set)
	}
	return nil
}

// SetElementList lists set elements.
type SetElementList struct {
	Elements []system.SetElement `json:"elements"`
}

// ListFirewallElements lists the addresses in a set.
var ListFirewallElements = Register(Def[SetRefInput, SetElementList]{
	Name:     "ListFirewallElements",
	Summary:  "List the addresses in a firewall set",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, in SetRefInput) (SetElementList, error) {
		elems, err := env.System.Firewall.Elements(ctx, in.Set)
		if elems == nil {
			elems = []system.SetElement{}
		}
		return SetElementList{Elements: elems}, err
	},
})

// NetworkConfigInput is the complete set of networkd files Racksail manages.
type NetworkConfigInput struct {
	Files map[string]string `json:"files" doc:"File name to contents, as NetworkConfig renders them: 50-racksail-<name>.network and .netdev files"`
	// ConfirmWithin applies the change on probation: unless
	// ConfirmNetworkConfig is called within this many seconds, the agent
	// puts the previous files back by itself. Zero applies it for good.
	ConfirmWithin int `json:"confirmWithin,omitempty" doc:"Seconds to wait for confirmation before rolling back; zero keeps the change without asking"`
}

// Validate implements Input. Every file is read the way networkd reads it
// and held to the sections and directives Racksail renders.
func (in NetworkConfigInput) Validate() error {
	if len(in.Files) > 256 {
		return fmt.Errorf("give at most 256 networkd files")
	}
	for name, content := range in.Files {
		if len(content) > 64<<10 {
			return fmt.Errorf("networkd file %s is too large", name)
		}
		if err := system.CheckNetworkdFile(name, []byte(content)); err != nil {
			return err
		}
	}
	if in.ConfirmWithin < 0 || in.ConfirmWithin > 3600 {
		return fmt.Errorf("confirmWithin must be between 0 and 3600 seconds")
	}
	return nil
}

// NetworkApplyResult is what applying network configuration did.
type NetworkApplyResult struct {
	Result
	// Pending is set when the change must be confirmed.
	Pending *NetworkChange `json:"pending,omitempty"`
}

// ApplyNetworkConfig replaces Racksail's systemd-networkd files and reloads
// networkd. With confirmWithin the change is safe to try: a change that cuts
// the panel off is rolled back by the agent on its own.
var ApplyNetworkConfig = Register(Def[NetworkConfigInput, NetworkApplyResult]{
	Name:    "ApplyNetworkConfig",
	Summary: "Apply network configuration",
	Description: "Replaces the systemd-networkd files Racksail manages and reloads networkd. With confirmWithin, the previous " +
		"files come back automatically unless ConfirmNetworkConfig is called in time, so a change that cuts the server off undoes itself.",
	Effects: func(in NetworkConfigInput) []system.Effect {
		return networkEffects(in.Files)
	},
	Run: func(ctx context.Context, env *Env, in NetworkConfigInput) (NetworkApplyResult, error) {
		current, err := env.System.Network.Config(ctx)
		if err != nil {
			return NetworkApplyResult{}, err
		}
		want := networkFiles(in.Files)
		if maps.EqualFunc(current, want, bytes.Equal) {
			return NetworkApplyResult{}, nil
		}
		effects := networkEffects(in.Files)
		if env.Check {
			return NetworkApplyResult{Result: Changed(effects...)}, nil
		}
		if in.ConfirmWithin == 0 {
			// Applying for good still has to refuse while a change waits to be
			// confirmed, or that change's rollback would undo this one later.
			if env.Runtime != nil {
				return NetworkApplyResult{Result: Changed(effects...)}, env.Runtime.applyNetworkForGood(ctx, want)
			}
			return NetworkApplyResult{Result: Changed(effects...)}, env.System.Network.ApplyConfig(ctx, want)
		}
		if env.Runtime == nil {
			return NetworkApplyResult{}, errNoRuntime
		}
		change, err := env.Runtime.applyNetwork(ctx, want, time.Duration(in.ConfirmWithin)*time.Second)
		if err != nil {
			return NetworkApplyResult{}, err
		}
		return NetworkApplyResult{Result: Changed(effects...), Pending: &change}, nil
	},
})

// ConfirmNetworkInput keeps a network change applied on probation.
type ConfirmNetworkInput struct {
	Token string `json:"token" doc:"The token ApplyNetworkConfig returned"`
}

var tokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Validate implements Input.
func (in ConfirmNetworkInput) Validate() error {
	if !tokenPattern.MatchString(in.Token) {
		return fmt.Errorf("the token isn't one ApplyNetworkConfig returns")
	}
	return nil
}

// ConfirmNetworkConfig keeps a network change, cancelling its rollback.
var ConfirmNetworkConfig = Register(Def[ConfirmNetworkInput, Result]{
	Name:    "ConfirmNetworkConfig",
	Summary: "Keep a network change",
	Description: "Confirms a network change applied with confirmWithin, so it isn't rolled back. The browser confirms through the panel once it can still reach it. " +
		"Confirming the same change again changes nothing; a change that was already rolled back, or a token when no change is waiting, is a conflict.",
	Effects: func(in ConfirmNetworkInput) []system.Effect {
		return []system.Effect{{Type: system.EffectNetwork, Action: "confirm", Target: "systemd-networkd"}}
	},
	Run: func(ctx context.Context, env *Env, in ConfirmNetworkInput) (Result, error) {
		if env.Runtime == nil {
			return Result{}, errNoRuntime
		}
		confirmed, err := env.Runtime.confirmNetwork(ctx, in.Token, env.Check)
		if err != nil || !confirmed {
			return Unchanged(), err
		}
		return Changed(system.Effect{Type: system.EffectNetwork, Action: "confirm", Target: "systemd-networkd"}), nil
	},
})

// NetworkStatus reports a network change waiting for confirmation.
type NetworkStatus struct {
	Pending *NetworkChange `json:"pending,omitempty"`
}

// GetNetworkChange reports whether a network change waits for confirmation.
var GetNetworkChange = Register(Def[Empty, NetworkStatus]{
	Name:     "GetNetworkChange",
	Summary:  "Report a network change waiting for confirmation",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, _ Empty) (NetworkStatus, error) {
		if env.Runtime == nil {
			return NetworkStatus{}, errNoRuntime
		}
		st, err := env.Runtime.networkStatus(ctx)
		return NetworkStatus{Pending: st}, err
	},
})

func networkEffects(files map[string]string) []system.Effect {
	names := slices.Sorted(maps.Keys(files))
	effects := []system.Effect{{Type: system.EffectNetwork, Action: "reload", Target: "systemd-networkd"}}
	for _, n := range names {
		effects = append(effects, system.Effect{Type: system.EffectFile, Action: "write", Target: system.NetworkdDir + "/" + n})
	}
	return effects
}

// HostnameInput is a new hostname.
type HostnameInput struct {
	Name string `json:"name" doc:"Hostname, without the domain"`
}

var hostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Validate implements Input.
func (in HostnameInput) Validate() error {
	if !hostnamePattern.MatchString(in.Name) {
		return fmt.Errorf("hostname %q must be lowercase letters, digits and hyphens", in.Name)
	}
	return nil
}

// SetHostname changes the machine's hostname.
var SetHostname = Register(Def[HostnameInput, Result]{
	Name:    "SetHostname",
	Summary: "Change the hostname",
	Effects: func(in HostnameInput) []system.Effect {
		return []system.Effect{{Type: system.EffectNetwork, Action: "hostname", Target: in.Name}}
	},
	Run: func(ctx context.Context, env *Env, in HostnameInput) (Result, error) {
		if cur, err := env.System.Network.Hostname(ctx); err == nil && cur == in.Name {
			return Unchanged(), nil
		}
		effect := system.Effect{Type: system.EffectNetwork, Action: "hostname", Target: in.Name}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Network.SetHostname(ctx, in.Name)
	},
})

// NetworkState describes interfaces and routes.
type NetworkState struct {
	Hostname   string             `json:"hostname"`
	Interfaces []system.Interface `json:"interfaces"`
	Routes     []system.Route     `json:"routes"`
}

// GetNetworkState reports interfaces, addresses and routes.
var GetNetworkState = Register(Def[Empty, NetworkState]{
	Name:     "GetNetworkState",
	Summary:  "Report interfaces and routes",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, _ Empty) (NetworkState, error) {
		n := env.System.Network
		var st NetworkState
		var err error
		if st.Hostname, err = n.Hostname(ctx); err != nil {
			return st, err
		}
		if st.Interfaces, err = n.Interfaces(ctx); err != nil {
			return st, err
		}
		if st.Routes, err = n.Routes(ctx); err != nil {
			return st, err
		}
		sort.Slice(st.Interfaces, func(i, j int) bool { return st.Interfaces[i].Index < st.Interfaces[j].Index })
		return st, nil
	},
})
