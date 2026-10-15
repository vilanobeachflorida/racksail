package linux

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

type firewall struct{ h *host }

// racksailTables lists the tables Racksail owns in the running ruleset.
func (f *firewall) racksailTables(ctx context.Context) ([]system.RulesetTable, error) {
	out, err := f.h.exec(ctx, progNft, "--json", "list", "tables")
	if err != nil {
		return nil, fmt.Errorf("listing nftables tables: %w", err)
	}
	return parseNftTables(out)
}

func parseNftTables(out []byte) ([]system.RulesetTable, error) {
	var doc struct {
		Nftables []struct {
			Table *struct {
				Family string `json:"family"`
				Name   string `json:"name"`
			} `json:"table"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("reading nft output: %w", err)
	}
	var tables []system.RulesetTable
	for _, item := range doc.Nftables {
		if item.Table != nil && strings.HasPrefix(item.Table.Name, "racksail") {
			tables = append(tables, system.RulesetTable{Family: item.Table.Family, Name: item.Table.Name})
		}
	}
	return tables, nil
}

// script builds one nft transaction that deletes Racksail's current tables
// and creates the new ones, so the switch is atomic. Addresses in sets with
// timeouts, such as bans, survive the switch with the time they had left
// when the new ruleset declares the same sets.
func (f *firewall) script(ctx context.Context, ruleset []byte) ([]byte, error) {
	tables, err := system.CheckRuleset(ruleset)
	if err != nil {
		return nil, err
	}
	current, err := f.racksailTables(ctx)
	if err != nil {
		return nil, err
	}
	kept, err := f.keptElements(ctx, tables, ruleset)
	if err != nil {
		return nil, err
	}
	return buildNftScript(current, ruleset, kept), nil
}

// keptSet is a set whose elements are carried over to a new ruleset.
type keptSet struct {
	ref   system.SetRef
	elems []system.SetElement
}

func (f *firewall) keptElements(ctx context.Context, newTables []system.RulesetTable, ruleset []byte) ([]keptSet, error) {
	out, err := f.h.exec(ctx, progNft, "--json", "list", "sets")
	if err != nil {
		return nil, fmt.Errorf("listing nftables sets: %w", err)
	}
	var kept []keptSet
	for _, ref := range parseTimeoutSets(out) {
		if !rulesetDeclaresSet(newTables, ruleset, ref) {
			continue
		}
		elems, err := f.Elements(ctx, ref)
		if err != nil {
			return nil, err
		}
		if len(elems) > 0 {
			kept = append(kept, keptSet{ref: ref, elems: elems})
		}
	}
	return kept, nil
}

// parseTimeoutSets returns Racksail's sets with the timeout flag.
func parseTimeoutSets(out []byte) []system.SetRef {
	var doc struct {
		Nftables []struct {
			Set *struct {
				Family string   `json:"family"`
				Name   string   `json:"name"`
				Table  string   `json:"table"`
				Flags  []string `json:"flags"`
			} `json:"set"`
		} `json:"nftables"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return nil
	}
	var refs []system.SetRef
	for _, item := range doc.Nftables {
		s := item.Set
		if s == nil || !strings.HasPrefix(s.Table, "racksail") {
			continue
		}
		dynamic := false
		for _, fl := range s.Flags {
			dynamic = dynamic || fl == "dynamic"
		}
		for _, fl := range s.Flags {
			// Dynamic sets are rate-limit meters; starting them over is fine.
			if fl == "timeout" && !dynamic {
				refs = append(refs, system.SetRef{Family: s.Family, Table: s.Table, Set: s.Name})
			}
		}
	}
	return refs
}

func rulesetDeclaresSet(tables []system.RulesetTable, ruleset []byte, ref system.SetRef) bool {
	found := false
	for _, t := range tables {
		found = found || (t.Family == ref.Family && t.Name == ref.Table)
	}
	if !found {
		return false
	}
	return regexp.MustCompile(`\bset\s+` + regexp.QuoteMeta(ref.Set) + `\s*\{`).Match(ruleset)
}

func buildNftScript(current []system.RulesetTable, ruleset []byte, kept []keptSet) []byte {
	var b bytes.Buffer
	for _, t := range current {
		fmt.Fprintf(&b, "delete table %s %s\n", t.Family, t.Name)
	}
	b.Write(ruleset)
	if !bytes.HasSuffix(ruleset, []byte("\n")) {
		b.WriteByte('\n')
	}
	for _, k := range kept {
		list, err := elementList(k.elems, true)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "add element %s %s %s %s\n", k.ref.Family, k.ref.Table, k.ref.Set, list)
	}
	return b.Bytes()
}

func (f *firewall) Check(ctx context.Context, ruleset []byte) error {
	script, err := f.script(ctx, ruleset)
	if err != nil {
		return err
	}
	_, err = f.h.execInput(ctx, script, progNft, "--check", "--file", "-")
	return wrap("checking the ruleset", err)
}

func (f *firewall) Apply(ctx context.Context, ruleset []byte) error {
	script, err := f.script(ctx, ruleset)
	if err != nil {
		return err
	}
	_, err = f.h.execInput(ctx, script, progNft, "--file", "-")
	return wrap("applying the ruleset", err)
}

func (f *firewall) Ruleset(ctx context.Context) ([]byte, error) {
	tables, err := f.racksailTables(ctx)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	for _, t := range tables {
		out, err := f.h.exec(ctx, progNft, "list", "table", t.Family, t.Name)
		if err != nil {
			return nil, fmt.Errorf("listing table %s %s: %w", t.Family, t.Name, err)
		}
		b.Write(out)
	}
	return b.Bytes(), nil
}

func elementList(elems []system.SetElement, withTimeout bool) (string, error) {
	parts := make([]string, 0, len(elems))
	for _, e := range elems {
		if _, err := netip.ParseAddr(e.Value); err != nil {
			if _, err := netip.ParsePrefix(e.Value); err != nil {
				return "", fmt.Errorf("set element %q: %w", e.Value, system.ErrInvalid)
			}
		}
		part := e.Value
		if withTimeout && e.Timeout > 0 {
			part += " timeout " + strconv.FormatInt(int64(e.Timeout/time.Second), 10) + "s"
		}
		parts = append(parts, part)
	}
	return "{ " + strings.Join(parts, ", ") + " }", nil
}

func (f *firewall) elements(ctx context.Context, verb string, set system.SetRef, elems []system.SetElement) error {
	if !set.Valid() {
		return fmt.Errorf("set %s %s %s: %w", set.Family, set.Table, set.Set, system.ErrInvalid)
	}
	if len(elems) == 0 {
		return nil
	}
	list, err := elementList(elems, verb == "add")
	if err != nil {
		return err
	}
	_, err = f.h.exec(ctx, progNft, verb, "element", set.Family, set.Table, set.Set, list)
	return wrap(verb+" elements of set "+set.Set, err)
}

func (f *firewall) AddElements(ctx context.Context, set system.SetRef, elems ...system.SetElement) error {
	return f.elements(ctx, "add", set, elems)
}

func (f *firewall) DeleteElements(ctx context.Context, set system.SetRef, elems ...system.SetElement) error {
	return f.elements(ctx, "delete", set, elems)
}

func (f *firewall) Elements(ctx context.Context, set system.SetRef) ([]system.SetElement, error) {
	if !set.Valid() {
		return nil, fmt.Errorf("set %s %s %s: %w", set.Family, set.Table, set.Set, system.ErrInvalid)
	}
	out, err := f.h.exec(ctx, progNft, "--json", "list", "set", set.Family, set.Table, set.Set)
	if err != nil {
		return nil, fmt.Errorf("listing set %s: %w", set.Set, err)
	}
	return parseNftSet(out)
}

// parseNftSet reads the elements of a set from nft's JSON output. Elements
// are plain addresses, prefixes, or objects carrying a timeout.
func parseNftSet(out []byte) ([]system.SetElement, error) {
	var doc struct {
		Nftables []struct {
			Set *struct {
				Elem []json.RawMessage `json:"elem"`
			} `json:"set"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("reading nft output: %w", err)
	}
	var elems []system.SetElement
	for _, item := range doc.Nftables {
		if item.Set == nil {
			continue
		}
		for _, raw := range item.Set.Elem {
			e, ok := parseNftElement(raw)
			if ok {
				elems = append(elems, e)
			}
		}
	}
	sort.Slice(elems, func(i, j int) bool { return elems[i].Value < elems[j].Value })
	return elems, nil
}

func parseNftElement(raw json.RawMessage) (system.SetElement, bool) {
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return system.SetElement{Value: plain}, true
	}
	var obj struct {
		Prefix *struct {
			Addr string `json:"addr"`
			Len  int    `json:"len"`
		} `json:"prefix"`
		Elem *struct {
			Val     json.RawMessage `json:"val"`
			Expires int64           `json:"expires"`
		} `json:"elem"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return system.SetElement{}, false
	}
	switch {
	case obj.Prefix != nil:
		return system.SetElement{Value: obj.Prefix.Addr + "/" + strconv.Itoa(obj.Prefix.Len)}, true
	case obj.Elem != nil:
		e, ok := parseNftElement(obj.Elem.Val)
		if ok && obj.Elem.Expires > 0 {
			e.Timeout = time.Duration(obj.Elem.Expires) * time.Second
		}
		return e, ok
	}
	return system.SetElement{}, false
}
