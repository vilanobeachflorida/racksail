package agent

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// The agent remembers what it wrote for each resource: one small file per
// target in its state directory, so recording a write never rewrites the
// rest. A periodic check compares the machine with that ledger and tells
// the panel, once, about every file or unit that was changed or removed by
// hand and every one of Racksail's units that failed; the panel then puts
// back what drifted. Files are compared by size and modification time
// first and only read when those changed, while a rolling full comparison,
// a few files per check, catches edits that kept both.

// Ledger kinds.
const (
	ledgerFile   = "file"
	ledgerUnit   = "unit"
	ledgerDropIn = "dropin"
)

// ledgerDir holds the ledger in the state directory.
const ledgerDir = "ledger"

// driftHashBudget is about how much of the files that look unchanged one
// check reads in full.
const driftHashBudget = 32 << 20

// ledgerEntry is something an operation put on the machine for a resource.
type ledgerEntry struct {
	Owner    store.ID  `json:"owner"`
	Kind     string    `json:"kind"`
	SHA256   string    `json:"sha256"`
	Recorded time.Time `json:"recorded"`
	// Size and ModTime are what a file had when its hash was taken.
	Size    int64     `json:"size,omitempty"`
	ModTime time.Time `json:"modTime,omitzero"`
}

// ledgerRecord is one ledger file.
type ledgerRecord struct {
	Target string      `json:"target"`
	Entry  ledgerEntry `json:"entry"`
}

// ledgerName is the ledger file of a target.
func ledgerName(target string) string {
	s := sha256.Sum256([]byte(target))
	return path.Join(ledgerDir, hex.EncodeToString(s[:20])+".json")
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// loadLedger reads the ledger once. The caller holds rt.mu.
func (rt *Runtime) loadLedger(ctx context.Context) error {
	if rt.loaded {
		return nil
	}
	files, err := rt.backend.Files.ReadDir(ctx, rt.statePath(ledgerDir))
	if err != nil && !errors.Is(err, system.ErrNotFound) {
		return err
	}
	for _, f := range files {
		if !f.Mode.IsRegular() || path.Ext(f.Path) != ".json" {
			continue
		}
		data, err := rt.backend.Files.ReadFile(ctx, f.Path, 1<<20)
		if err != nil {
			return err
		}
		var r ledgerRecord
		if err := json.Unmarshal(data, &r); err != nil || r.Target == "" {
			rt.log.Warn("skipping a ledger file that can't be read", "file", f.Path, "error", err)
			continue
		}
		rt.ledger[r.Target] = r.Entry
	}
	rt.loaded = true
	return nil
}

func (rt *Runtime) saveEntry(ctx context.Context, target string, e ledgerEntry) error {
	data, err := json.Marshal(ledgerRecord{Target: target, Entry: e})
	if err != nil {
		return err
	}
	return rt.writeState(ctx, ledgerName(target), data)
}

// filesFor returns the view a target of the ledger is read through: below
// its managed root, without following links.
func (rt *Runtime) filesFor(target string) system.Files {
	if files, err := beneathRoot(rt.backend.Files, target, true); err == nil {
		return files
	}
	return rt.backend.Files
}

// remember records that target now holds content written for owner, so a
// later change by hand is reported as drift.
func (rt *Runtime) remember(ctx context.Context, kind, target string, owner store.ID, content []byte) error {
	e := ledgerEntry{Owner: owner, Kind: kind, SHA256: sum(content), Recorded: rt.now().UTC()}
	if kind == ledgerFile {
		if info, err := rt.filesFor(target).Stat(ctx, target); err == nil {
			e.Size, e.ModTime = info.Size, info.ModTime
		}
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if err := rt.loadLedger(ctx); err != nil {
		return err
	}
	if old, ok := rt.ledger[target]; ok && old.Owner == e.Owner && old.Kind == e.Kind && old.SHA256 == e.SHA256 &&
		old.Size == e.Size && old.ModTime.Equal(e.ModTime) {
		return nil
	}
	rt.ledger[target] = e
	rt.verified[target] = rt.now()
	return rt.saveEntry(ctx, target, e)
}

// forget drops what an operation removed from the ledger, and everything
// below a removed directory.
func (rt *Runtime) forget(ctx context.Context, targets ...string) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if err := rt.loadLedger(ctx); err != nil {
		return err
	}
	var errs []error
	for _, t := range targets {
		for key := range rt.ledger {
			if key == t || strings.HasPrefix(key, strings.TrimSuffix(t, "/")+"/") {
				delete(rt.ledger, key)
				delete(rt.verified, key)
				errs = append(errs, rt.removeState(ctx, ledgerName(key)))
			}
		}
	}
	return errors.Join(errs...)
}

// Drift is something on the machine that no longer matches what Racksail
// put there.
type Drift struct {
	Owner store.ID `json:"owner,omitempty" doc:"The resource it was written for"`
	// Kind is file, unit or dropin.
	Kind   string `json:"kind" enum:"file,unit,dropin"`
	Target string `json:"target" doc:"The path or unit"`
	// Reason is changed, missing or failed.
	Reason string `json:"reason" enum:"changed,missing,failed"`
}

// hashFile hashes a file's contents as it streams them.
func hashFile(ctx context.Context, files system.Files, p string) (string, error) {
	f, err := files.Open(ctx, p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CheckDrift compares the machine with the ledger and reports what was
// changed or removed by hand, and Racksail's units that failed.
func (rt *Runtime) CheckDrift(ctx context.Context) ([]Drift, error) {
	rt.mu.Lock()
	if err := rt.loadLedger(ctx); err != nil {
		rt.mu.Unlock()
		return nil, err
	}
	entries := make(map[string]ledgerEntry, len(rt.ledger))
	verified := make(map[string]time.Time, len(rt.verified))
	for k, v := range rt.ledger {
		entries[k] = v
		verified[k] = rt.verified[k]
	}
	rt.mu.Unlock()

	now := rt.now()
	out := []Drift{}
	report := func(target string, e ledgerEntry, reason string) {
		out = append(out, Drift{Owner: e.Owner, Kind: e.Kind, Target: target, Reason: reason})
	}
	// touched are files whose contents still match although their size or
	// time changed; checked are files compared in full.
	touched := map[string]ledgerEntry{}
	checked := map[string]bool{}
	var due []string
	for target, e := range entries {
		var content []byte
		var err error
		switch e.Kind {
		case ledgerUnit:
			content, err = rt.backend.Units.ReadUnit(ctx, target)
		case ledgerDropIn:
			content, err = rt.backend.Files.ReadFile(ctx, target, 1<<20)
		default:
			files := rt.filesFor(target)
			info, err := files.Stat(ctx, target)
			switch {
			case errors.Is(err, system.ErrNotFound):
				report(target, e, "missing")
			case errors.Is(err, system.ErrInvalid):
				// A link now stands where the file or one of its directories was.
				report(target, e, "changed")
			case err != nil:
				return nil, err
			case !info.Mode.IsRegular():
				report(target, e, "changed")
			case info.Size != e.Size || !info.ModTime.Equal(e.ModTime):
				h, err := hashFile(ctx, files, target)
				switch {
				case err != nil && (errors.Is(err, system.ErrNotFound) || errors.Is(err, system.ErrInvalid)):
					report(target, e, "missing")
				case err != nil:
					return nil, err
				case h != e.SHA256:
					report(target, e, "changed")
				default:
					e.Size, e.ModTime = info.Size, info.ModTime
					touched[target] = e
					checked[target] = true
				}
			default:
				due = append(due, target)
			}
			continue
		}
		switch {
		case errors.Is(err, system.ErrNotFound):
			report(target, e, "missing")
		case err != nil:
			return nil, err
		case sum(content) != e.SHA256:
			report(target, e, "changed")
		}
	}
	// The files that look unchanged are compared in full in turns, those
	// compared longest ago first.
	slices.SortFunc(due, func(a, b string) int { return verified[a].Compare(verified[b]) })
	budget := int64(driftHashBudget)
	for _, target := range due {
		if budget <= 0 {
			break
		}
		e := entries[target]
		budget -= max(e.Size, 1)
		h, err := hashFile(ctx, rt.filesFor(target), target)
		switch {
		case err != nil && (errors.Is(err, system.ErrNotFound) || errors.Is(err, system.ErrInvalid)):
			report(target, e, "missing")
		case err != nil:
			return nil, err
		case h != e.SHA256:
			report(target, e, "changed")
		default:
			checked[target] = true
		}
	}
	units, err := rt.backend.Units.List(ctx, "racksail-*")
	if err != nil {
		return nil, err
	}
	for _, u := range units {
		if u.Failed() {
			owner := store.ID("")
			if e, ok := entries[u.Name]; ok {
				owner = e.Owner
			}
			out = append(out, Drift{Owner: owner, Kind: ledgerUnit, Target: u.Name, Reason: "failed"})
		}
	}
	rt.mu.Lock()
	for target := range checked {
		if _, ok := rt.ledger[target]; ok {
			rt.verified[target] = now
		}
	}
	var errs []error
	for target, e := range touched {
		// Unless an operation recorded it again meanwhile.
		if cur, ok := rt.ledger[target]; ok && cur.SHA256 == e.SHA256 {
			rt.ledger[target] = e
			errs = append(errs, rt.saveEntry(ctx, target, e))
		}
	}
	rt.mu.Unlock()
	if err := errors.Join(errs...); err != nil {
		rt.log.Warn("updating the ledger", "error", err)
	}
	sortDrift(out)
	return out, nil
}

func sortDrift(d []Drift) {
	slices.SortFunc(d, func(a, b Drift) int {
		return cmp.Or(cmp.Compare(a.Target, b.Target), cmp.Compare(a.Reason, b.Reason))
	})
}

func driftKey(d Drift) string { return d.Kind + " " + d.Target }

// publishDrift tells subscribers about drift the last check found that the
// one before didn't, so the same drift isn't reported every minute. Drift
// that went away and comes back is reported again.
func (rt *Runtime) publishDrift(found []Drift) {
	rt.driftMu.Lock()
	next := make(map[string]Drift, len(found))
	var fresh []Drift
	for _, d := range found {
		key := driftKey(d)
		next[key] = d
		if old, ok := rt.drift[key]; !ok || old != d {
			fresh = append(fresh, d)
		}
	}
	rt.drift = next
	rt.driftMu.Unlock()
	for _, d := range fresh {
		rt.events.publish(Event{Type: EventDrift, Time: rt.now().UTC(), Drift: &d})
	}
}

// CurrentDrift returns what the last periodic check found, for a panel
// that starts listening after it was published.
func (rt *Runtime) CurrentDrift() []Drift {
	rt.driftMu.Lock()
	defer rt.driftMu.Unlock()
	out := make([]Drift, 0, len(rt.drift))
	for _, d := range rt.drift {
		out = append(out, d)
	}
	sortDrift(out)
	return out
}
