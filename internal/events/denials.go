package events

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Denials bounds how many records of refused attempts one actor adds to the
// audit log. Without a bound a caller that keeps being refused, by mistake
// or on purpose, at the rate the API allows would push everyone else's
// records out of the log long before they are due to go.
type Denials struct {
	// Window is how long the refusals of an actor are counted together: a
	// refusal the same as one already recorded within it only adds to that
	// record's Repeats. The default is an hour.
	Window time.Duration
	// PerActor caps the different refusals recorded one by one for an actor
	// within a window; the rest count together on one record of the action
	// RefusalsAction. The default is 100.
	PerActor int
}

func (d Denials) withDefaults() Denials {
	if d.Window <= 0 {
		d.Window = time.Hour
	}
	if d.PerActor <= 0 {
		d.PerActor = 100
	}
	return d
}

// RefusalsAction is the action of the audit record that counts an actor's
// refusals beyond those Denials records one by one.
const RefusalsAction = "core.refusals"

// denials remembers, per actor and window, which refusals have a record
// already, so repeats only count on it.
type denials struct {
	limits Denials

	mu     sync.Mutex
	actors map[string]*actorDenials
}

// actorDenials are the records of one actor's refusals in the current
// window.
type actorDenials struct {
	since   time.Time
	records map[string]*denialRecord
	// overflow is the record that counts the refusals beyond the cap.
	overflow *denialRecord
}

// denialRecord is a record repeats count on. Until it is written, repeats
// count here and its writer adds them, so no one waits for another's write.
type denialRecord struct {
	id      store.ID
	written bool
	pending int
	last    time.Time
}

// maxActors bounds the actors the log remembers refusals of; past it, the
// windows start over, which only means some repeats get records of their
// own.
const maxActors = 100_000

// denialKey names whose refusals count together: the actor when it is
// known, else the address the attempts come from.
func denialKey(a auth.Actor) string {
	switch {
	case a.ID != "":
		return string(a.Type) + ":" + string(a.ID)
	case a.IP != "":
		return string(a.Type) + "@" + a.IP
	}
	return string(a.Type) + ":" + a.Name
}

// denialSignature tells refusals apart: the same actor, as far as the record
// says, refused the same thing on the same resource for the same reason is
// a repeat.
func denialSignature(r *AuditRecord) string {
	a := r.Actor
	parts := []string{
		string(a.Type), string(a.ID), a.Name, string(a.User), string(a.Source), string(a.Impersonator), a.IP,
		r.Action, r.Kind, string(r.Subject), r.Scope.String(), string(r.ChangeSet), r.Summary, r.Reason,
	}
	var sig []byte
	for _, p := range parts {
		sig = append(sig, p...)
		sig = append(sig, 0)
	}
	return string(sig)
}

// plan says what to do with a refusal. It returns the record to write for
// it, when there is one, and the denialRecord to hand to wrote afterwards;
// or the denialRecord the refusal is a repeat on; or neither, when the
// refusal was counted on a record that is still being written.
func (d *denials) plan(r *AuditRecord) (on *denialRecord, write *AuditRecord) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := denialKey(r.Actor)
	a := d.actors[key]
	if a == nil || r.Time.Sub(a.since) >= d.limits.Window || r.Time.Before(a.since) {
		if len(d.actors) >= maxActors {
			d.actors = map[string]*actorDenials{}
		}
		a = &actorDenials{since: r.Time, records: map[string]*denialRecord{}}
		d.actors[key] = a
	}
	sig := denialSignature(r)
	on = a.records[sig]
	if on == nil && len(a.records) < d.limits.PerActor {
		on = &denialRecord{id: r.ID}
		a.records[sig] = on
		return on, r
	}
	if on == nil && a.overflow == nil {
		a.overflow = &denialRecord{id: store.NewID(AuditPrefix)}
		return a.overflow, d.overflowRecord(a.overflow.id, r)
	}
	if on == nil {
		on = a.overflow
	}
	if !on.written {
		on.pending++
		on.last = later(on.last, r.Time)
		return nil, nil
	}
	return on, nil
}

// overflowRecord is the record the refusals of an actor beyond the cap
// count on, starting with r.
func (d *denials) overflowRecord(id store.ID, r *AuditRecord) *AuditRecord {
	details, _ := json.Marshal(map[string]any{"recordedOneByOne": d.limits.PerActor, "windowSeconds": int64(d.limits.Window / time.Second)})
	return &AuditRecord{
		ID: id, Time: r.Time, Actor: r.Actor, Action: RefusalsAction, Scope: store.ServerScope(), Outcome: OutcomeDenied,
		Summary:   "Further refusals, counted together",
		Reason:    "The audit log records only so many different refusals of one actor at a time one by one; the rest count on this record.",
		Details:   details,
		RequestID: r.RequestID,
	}
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// wrote marks the record of on as written, or forgets it when it couldn't
// be, and returns the repeats that counted on it meanwhile.
func (d *denials) wrote(r *AuditRecord, on *denialRecord, ok bool) (int, time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n, last := on.pending, on.last
	on.pending = 0
	if ok {
		on.written = true
	} else {
		d.dropLocked(r, on)
	}
	return n, last
}

// gone forgets a record a repeat found missing, as when it was pruned or
// the transaction that wrote it rolled back, so the refusal is planned again
// without it.
func (d *denials) gone(r *AuditRecord, on *denialRecord) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dropLocked(r, on)
}

func (d *denials) dropLocked(r *AuditRecord, on *denialRecord) {
	a := d.actors[denialKey(r.Actor)]
	if a == nil {
		return
	}
	if sig := denialSignature(r); a.records[sig] == on {
		delete(a.records, sig)
	}
	if a.overflow == on {
		a.overflow = nil
	}
}

// forget drops the windows that have ended.
func (d *denials) forget(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for key, a := range d.actors {
		if now.Sub(a.since) >= d.limits.Window {
			delete(d.actors, key)
		}
	}
}

// recordDenial writes a refusal, or counts it on the record of the same
// refusal or of the actor's others, as Denials says.
func (l *Log) recordDenial(ctx context.Context, r *AuditRecord) error {
	for range 2 {
		on, write := l.denials.plan(r)
		switch {
		case write != nil:
			err := l.storage.appendAudit(ctx, write)
			n, last := l.denials.wrote(r, on, err == nil)
			if err == nil && n > 0 {
				_, err = l.storage.repeatAudit(ctx, on.id, n, last)
			}
			return err
		case on == nil:
			return nil
		}
		found, err := l.storage.repeatAudit(ctx, on.id, 1, r.Time)
		if err != nil || found {
			return err
		}
		l.denials.gone(r, on)
	}
	return l.storage.appendAudit(ctx, r)
}
