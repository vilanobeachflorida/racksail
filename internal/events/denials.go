package events

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
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
	// most bounds the records remembered across actors, and so the memory
	// they take: past it every window starts over, which only means some
	// repeats get records of their own.
	most int

	mu     sync.Mutex
	actors map[string]*actorDenials
	// tracked counts the records remembered.
	tracked int
}

// maxTracked is the records the log remembers refusals by.
const maxTracked = 250_000

func newDenials(limits Denials) *denials {
	return &denials{limits: limits.withDefaults(), most: maxTracked, actors: map[string]*actorDenials{}}
}

// actorDenials are the records of one actor's refusals in the current
// window.
type actorDenials struct {
	since   time.Time
	records map[denialSig]*denialRecord
	// overflow is the record that counts the refusals beyond the cap.
	overflow *denialRecord
}

// size is how many records the window remembers.
func (a *actorDenials) size() int {
	if a.overflow != nil {
		return len(a.records) + 1
	}
	return len(a.records)
}

// denialRecord is a record repeats count on. Until it is written, repeats
// count here and its writer adds them, so no one waits for another's write.
type denialRecord struct {
	id      store.ID
	written bool
	pending int
	last    time.Time
}

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

// denialSig tells refusals apart: the same actor, as far as the record says,
// refused the same thing on the same resource for the same reason is a
// repeat. It is a hash of all that, so remembering it takes little memory;
// each part goes in with its length, and the hash is a cryptographic one,
// so no refusal can be made to pass for another.
type denialSig [16]byte

func denialSignature(r *AuditRecord) denialSig {
	a := r.Actor
	h := sha256.New()
	for _, p := range []string{
		string(a.Type), string(a.ID), a.Name, string(a.User), string(a.Source), string(a.Impersonator), a.IP,
		r.Action, r.Kind, string(r.Subject), r.Scope.String(), string(r.ChangeSet), r.Summary, r.Reason,
	} {
		h.Write(binary.AppendUvarint(nil, uint64(len(p))))
		h.Write([]byte(p))
	}
	var sig denialSig
	copy(sig[:], h.Sum(nil))
	return sig
}

// plan says what to do with a refusal. It returns the record to write for
// it, when there is one, and the denialRecord to hand to wrote afterwards;
// or the denialRecord the refusal is a repeat on; or neither, when the
// refusal was counted on a record that is still being written.
func (d *denials) plan(r *AuditRecord) (on *denialRecord, write *AuditRecord) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key, sig := denialKey(r.Actor), denialSignature(r)
	a := d.window(key, r.Time)
	on = a.records[sig]
	if on == nil && (len(a.records) < d.limits.PerActor || a.overflow == nil) {
		// A record to remember: the refusal's own, or the one of the
		// actor's refusals past the cap.
		if d.tracked >= d.most {
			d.actors, d.tracked = map[string]*actorDenials{}, 0
			a = d.window(key, r.Time)
		}
		d.tracked++
		if len(a.records) < d.limits.PerActor {
			on = &denialRecord{id: r.ID}
			a.records[sig] = on
			return on, r
		}
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

// window returns the actor's current window, starting a new one when the
// last has ended.
func (d *denials) window(key string, at time.Time) *actorDenials {
	a := d.actors[key]
	if a != nil && at.Sub(a.since) < d.limits.Window && !at.Before(a.since) {
		return a
	}
	if a != nil {
		d.tracked -= a.size()
	}
	a = &actorDenials{since: at, records: map[denialSig]*denialRecord{}}
	d.actors[key] = a
	return a
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
		d.tracked--
	}
	if a.overflow == on {
		a.overflow = nil
		d.tracked--
	}
}

// forget drops the windows that have ended.
func (d *denials) forget(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for key, a := range d.actors {
		if now.Sub(a.since) >= d.limits.Window {
			d.tracked -= a.size()
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
