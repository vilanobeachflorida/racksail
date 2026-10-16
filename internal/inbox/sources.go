package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Permissions of the items that aren't about a resource or a job.
const (
	// PermServerRead covers the server itself: its updates and the
	// certificates no resource owns.
	PermServerRead auth.Permission = "core.server.read"
	// PermModuleRead covers the modules and suggestions about them.
	PermModuleRead auth.Permission = "core.module.read"
	// PermEventRead covers alerts, as it covers the timeline they come from.
	PermEventRead auth.Permission = "core.event.read"
)

// version makes an item's version from what makes it what it is.
func version(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// objectsOf returns the resources an actor holds grants on one at a time,
// so sources can still narrow what they read for someone whose grants
// cover no whole owner.
func objectsOf(a *auth.Actor) []store.ID {
	if a == nil {
		return nil
	}
	var ids []store.ID
	for _, g := range a.Grants {
		if g.Object != "" {
			ids = append(ids, g.Object)
		}
	}
	return ids
}

// FailedJobs finds the work that failed recently and wasn't run again or
// undone since.
type FailedJobs struct {
	Queue jobs.Queue
	// ChangeSets reads change sets, so the failed change of a change set
	// that was undone, or carried out by a retry, drops out. Nil keeps
	// them.
	ChangeSets func(ctx context.Context, id store.ID) (*changeset.ChangeSet, error)
	// Window is how far back to look; a week when zero.
	Window time.Duration
}

// Items implements Source.
func (f FailedJobs) Items(ctx context.Context, q Query) ([]Item, error) {
	window := f.Window
	if window <= 0 {
		window = 7 * 24 * time.Hour
	}
	var visible *jobs.Visibility
	if !q.All {
		visible = &jobs.Visibility{Scopes: q.Scopes, Person: jobs.Of(q.Actor)}
	}
	// The latest jobs of any state tell which failed ones ran again.
	recent, _, err := f.Queue.List(ctx, jobs.Filter{Visible: visible, Limit: 500})
	if err != nil {
		return nil, err
	}
	retried := map[store.ID]bool{}
	for _, j := range recent {
		if j.RetryOf != "" {
			retried[j.RetryOf] = true
		}
	}
	failed, _, err := f.Queue.List(ctx, jobs.Filter{State: jobs.StateFailed, Visible: visible, Limit: 100})
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, j := range failed {
		when := j.CreatedAt
		if j.FinishedAt != nil {
			when = *j.FinishedAt
		}
		if q.Now.Sub(when) > window || retried[j.ID] {
			continue
		}
		undo := false
		if j.ChangeSet != "" && f.ChangeSets != nil {
			cs, err := f.ChangeSets(ctx, j.ChangeSet)
			switch {
			case i18n.CodeOf(err) == i18n.ErrNotFound.Code:
				// No longer kept, so nothing about it can be done here.
				continue
			case err != nil:
				return nil, err
			case cs.State != changeset.StateFailed:
				continue
			}
			undo = len(cs.Changes) > 0
		}
		message := ""
		if j.Error != nil {
			message = j.Error.Message
		}
		it := Item{
			ID: "job:" + string(j.ID), Category: CategoryJob, Severity: events.SeverityError,
			Code: "job_failed", Params: map[string]string{"title": j.Title, "error": message},
			Title: j.Title + " failed", Message: message, Time: when,
			Subject: j.Resource, Scope: j.Scope, Job: j.ID, ChangeSet: j.ChangeSet,
			Version: string(j.ID), Access: Access{Job: j},
		}
		it.Actions = append(it.Actions, Action{Type: ActionRetryJob, Title: "Retry", Target: string(j.ID)})
		if undo {
			it.Actions = append(it.Actions, Action{Type: ActionUndoChangeSet, Title: "Undo the change", Target: string(j.ChangeSet)})
		}
		it.Actions = append(it.Actions, Action{Type: ActionOpenJob, Title: "Show the log", Target: string(j.ID)})
		out = append(out, it)
	}
	return out, nil
}

// Resources finds resources whose reconciler reports that they fail or
// work only in part.
type Resources struct {
	Store store.Reader
	Kinds *store.KindRegistry
	// Limit bounds how many of each kind are shown; 100 when zero.
	Limit int
}

// Items implements Source.
func (r Resources) Items(ctx context.Context, q Query) ([]Item, error) {
	limit := r.Limit
	if limit <= 0 {
		limit = 100
	}
	base := store.Query{Phases: []store.Phase{store.PhaseFailed, store.PhaseDegraded}, Sort: "-" + store.SortUpdatedAt, Limit: limit}
	if !q.All {
		base.Scopes = q.Scopes
		if len(q.Scopes) == 0 {
			if base.IDs = objectsOf(q.Actor); len(base.IDs) == 0 {
				return nil, nil
			}
		}
	}
	var out []Item
	for _, k := range r.Kinds.All() {
		info := k.Info()
		query := base
		query.Kind = info.Name
		page, err := r.Store.List(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, res := range page.Items {
			out = append(out, resourceItem(info, res))
		}
	}
	return out, nil
}

// resourceItem describes a resource that needs attention.
func resourceItem(info *store.KindInfo, res *store.Resource) Item {
	st, _ := res.CommonStatus()
	name := titleOf(info, res)
	it := Item{
		ID: "resource:" + string(res.ID), Category: CategoryResource, Severity: events.SeverityError,
		Code: "resource_failed", Params: map[string]string{"kind": info.Title, "name": name, "message": st.Message},
		Title: info.Title + " " + name + " isn't working", Message: st.Message, Time: res.UpdatedAt,
		Subject: res.ID, Kind: res.Kind, Scope: res.Scope,
		Version: version(string(st.Phase), string(st.Code), st.Message, strconv.FormatInt(st.ObservedGeneration, 10)),
		Access:  Access{Resource: &res.Meta},
	}
	if st.Phase == store.PhaseDegraded {
		it.Severity, it.Code, it.Title = events.SeverityWarning, "resource_degraded", info.Title+" "+name+" works only in part"
	}
	// The time it stopped working is when its Ready condition last changed.
	if c, ok := st.Condition("Ready"); ok && !c.LastTransitionTime.IsZero() {
		it.Time = c.LastTransitionTime
	}
	if !info.UI.Hidden {
		it.Actions = append(it.Actions, Action{Type: ActionOpenResource, Title: "Open", Target: string(res.ID)})
	}
	it.Actions = append(it.Actions, Action{Type: ActionOpenEvents, Title: "Show its events", Target: string(res.ID)})
	return it
}

// titleOf returns what a resource is called: its kind's title field, or
// its ID.
func titleOf(info *store.KindInfo, res *store.Resource) string {
	if info.UI.TitleField != "" {
		var v any
		if json.Unmarshal(res.Spec, &v) == nil {
			for _, key := range strings.Split(info.UI.TitleField, ".") {
				m, _ := v.(map[string]any)
				v = m[key]
			}
			if list, ok := v.([]any); ok && len(list) > 0 {
				v = list[0]
			}
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return string(res.ID)
}

// EventLog is the part of the timeline alerts read.
type EventLog interface {
	QueryEvents(ctx context.Context, q events.Query) ([]events.Event, string, error)
}

// Alerts finds recent events that are warnings or worse, one item for each
// type and subject. Failures of resources and change sets are left to the
// sources that know whether they still matter.
type Alerts struct {
	Events EventLog
	// Window is how far back to look; three days when zero.
	Window time.Duration
}

// Items implements Source.
func (a Alerts) Items(ctx context.Context, q Query) ([]Item, error) {
	window := a.Window
	if window <= 0 {
		window = 72 * time.Hour
	}
	query := events.Query{Since: q.Now.Add(-window), MinSeverity: events.SeverityWarning, Limit: store.MaxLimit}
	if !q.All {
		if len(q.Scopes) == 0 {
			return nil, nil
		}
		query.Scopes = q.Scopes
	}
	list, _, err := a.Events.QueryEvents(ctx, query)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*Item{}
	var order []string
	for i := range list {
		e := list[i]
		if !alertWorthy(e) {
			continue
		}
		key := "alert:" + e.Type
		if e.Subject != "" {
			key += ":" + string(e.Subject)
		}
		it, ok := byKey[key]
		if !ok {
			// Events come newest first, so the first one is the latest.
			it = &Item{
				ID: key, Category: CategoryAlert, Severity: e.Severity, Code: "alert",
				Title: e.Message, Time: e.Time, Subject: e.Subject, Kind: e.Kind, Scope: e.Scope,
				Job: e.Job, ChangeSet: e.ChangeSet, Event: &e, Version: string(e.ID),
				Access: Access{Permission: PermEventRead, Scope: e.Scope},
			}
			if e.Subject != "" {
				it.Actions = append(it.Actions, Action{Type: ActionOpenResource, Title: "Open", Target: string(e.Subject)})
			}
			if e.Job != "" {
				it.Actions = append(it.Actions, Action{Type: ActionOpenJob, Title: "Show the log", Target: string(e.Job)})
			}
			target := e.Type
			if e.Subject != "" {
				target = string(e.Subject)
			}
			it.Actions = append(it.Actions, Action{Type: ActionOpenEvents, Title: "Show the events", Target: target})
			byKey[key] = it
			order = append(order, key)
		}
		it.Count++
		if e.Severity.AtLeast(it.Severity) {
			it.Severity = e.Severity
		}
	}
	out := make([]Item, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	return out, nil
}

// alertWorthy leaves out the events other sources cover: failures of
// resources, which show while the resource still fails, failed change sets,
// which show with their job, and drift Racksail already put right.
func alertWorthy(e events.Event) bool {
	switch {
	case e.Type == events.ChangeSetFailed.Name:
		return false
	case e.Kind != "" && e.Type == e.Kind+".failed":
		return false
	case e.Kind != "" && e.Type == e.Kind+".drifted":
		var d events.Drift
		return json.Unmarshal(e.Data, &d) != nil || !d.Corrected
	}
	return true
}

// Updates finds packages waiting to be upgraded on the server, a new
// kernel, and a restart that would finish installing updates.
type Updates struct {
	Agent agent.Client
}

// Items implements Source.
func (u Updates) Items(ctx context.Context, q Query) ([]Item, error) {
	server := Access{Permission: PermServerRead, Scope: store.ServerScope()}
	docs := Action{Type: ActionDocs, Title: "How updates work", Target: "install/updates"}
	plain, err := agent.UpgradablePackages.Call(ctx, u.Agent, agent.UpgradableInput{}, agent.CallOptions{})
	if err != nil {
		return nil, err
	}
	full, err := agent.UpgradablePackages.Call(ctx, u.Agent, agent.UpgradableInput{Full: true}, agent.CallOptions{})
	if err != nil {
		return nil, err
	}
	reboot, err := agent.RebootStatus.Call(ctx, u.Agent, agent.Empty{}, agent.CallOptions{})
	if err != nil {
		return nil, err
	}
	var out []Item
	if n := len(plain.Packages); n > 0 {
		security := 0
		var names []string
		for _, p := range plain.Packages {
			if strings.Contains(strings.ToLower(p.Origin), "security") {
				security++
			}
			names = append(names, p.Name+"="+p.Version)
		}
		it := Item{
			ID: "updates:packages", Category: CategoryUpdate, Severity: events.SeverityNotice, Code: "updates_available",
			Params: map[string]string{"count": strconv.Itoa(n), "security": strconv.Itoa(security)},
			Title:  plural(n, "A package update is available", "%d package updates are available"),
			Time:   q.Now, Scope: store.ServerScope(), Version: version(names...), Access: server,
			Actions: []Action{docs},
		}
		if security > 0 {
			it.Severity = events.SeverityWarning
			it.Message = plural(security, "One of them fixes security problems.", "%d of them fix security problems.")
			if security == n {
				it.Message = plural(n, "It fixes security problems.", "They all fix security problems.")
			}
		}
		out = append(out, it)
	}
	for _, p := range full.Packages {
		if !strings.HasPrefix(p.Name, "linux-image-") || slices.ContainsFunc(plain.Packages, func(q system.Package) bool { return q.Name == p.Name }) {
			continue
		}
		out = append(out, Item{
			ID: "updates:kernel", Category: CategoryUpdate, Severity: events.SeverityNotice, Code: "kernel_available",
			Params: map[string]string{"version": p.Version},
			Title:  "A new Linux kernel, " + p.Version + ", is available", Message: "Installing it means restarting the server.",
			Time: q.Now, Scope: store.ServerScope(), Version: version(p.Name, p.Version), Access: server, Actions: []Action{docs},
		})
		break
	}
	if reboot.Required {
		it := Item{
			ID: "updates:restart", Category: CategoryUpdate, Severity: events.SeverityWarning, Code: "restart_needed",
			Params: map[string]string{"reasons": strings.Join(reboot.Reasons, "; ")},
			Title:  "Restart the server to finish installing updates", Message: capitalize(strings.Join(reboot.Reasons, "; ")) + ".",
			Time: q.Now, Scope: store.ServerScope(), Version: version(append([]string{reboot.Newest}, reboot.Reasons...)...),
			Access: server, Actions: []Action{docs},
		}
		if reboot.Scheduled != nil {
			at := reboot.Scheduled.UTC()
			it.Severity, it.Code = events.SeverityInfo, "restart_scheduled"
			it.Params["time"] = at.Format(time.RFC3339)
			it.Title = "The server restarts at " + at.Format("2006-01-02 15:04 UTC") + " to finish installing updates"
			it.Version = version(it.Version, at.Format(time.RFC3339))
		}
		out = append(out, it)
	}
	return out, nil
}

// plural picks the singular text for one and fills the plural one with n
// otherwise, for English titles; the UI has its own messages.
func plural(n int, one, other string) string {
	if n == 1 {
		return one
	}
	if strings.Contains(other, "%d") {
		return fmt.Sprintf(other, n)
	}
	return other
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Certificates finds certificates that couldn't be obtained or renewed,
// expired, or expire soon: those the edge serves and those installed for
// other services, such as mail.
type Certificates struct {
	// Edge lists the edge's certificates; nil when there is no edge to ask.
	Edge func(ctx context.Context) ([]edge.Certificate, error)
	// Agent lists the certificates installed on the server; nil leaves
	// them out.
	Agent agent.Client
	// Store finds the resources that use a certificate, for who may see it.
	Store store.Reader
	// Soon is how close to expiring a certificate gets an item; two weeks
	// when zero. Certificates Racksail obtains renew a month before.
	Soon time.Duration
}

// Items implements Source.
func (c Certificates) Items(ctx context.Context, q Query) ([]Item, error) {
	soon := c.Soon
	if soon <= 0 {
		soon = 14 * 24 * time.Hour
	}
	var out []Item
	if c.Edge != nil {
		certs, err := c.Edge(ctx)
		if err != nil {
			return nil, err
		}
		for _, cert := range certs {
			it, ok := c.edgeItem(ctx, cert, q.Now, soon)
			if ok {
				out = append(out, it)
			}
		}
	}
	if c.Agent != nil {
		installed, err := agent.ListCertificates.Call(ctx, c.Agent, agent.Empty{}, agent.CallOptions{})
		if err != nil {
			return nil, err
		}
		for _, cert := range installed.Certificates {
			left := cert.NotAfter.Sub(q.Now)
			if left > soon {
				continue
			}
			it := certificateItem("certificate:installed:"+cert.Name, cert.Name, cert.NotAfter, q.Now)
			it.Version = version(cert.FingerprintSHA256)
			it.Access = Access{Permission: PermServerRead, Scope: store.ServerScope()}
			out = append(out, it)
		}
	}
	return out, nil
}

// edgeItem describes a certificate of the edge that needs attention.
func (c Certificates) edgeItem(ctx context.Context, cert edge.Certificate, now time.Time, soon time.Duration) (Item, bool) {
	var it Item
	switch {
	case cert.Status == edge.CertFailed:
		it = Item{
			ID: "certificate:" + cert.Subject, Category: CategoryCertificate, Severity: events.SeverityError, Code: "certificate_failed",
			Params: map[string]string{"name": cert.Subject, "error": cert.Error},
			Title:  "The certificate for " + cert.Subject + " couldn't be obtained", Message: cert.Error, Time: now,
		}
	case cert.Status == edge.CertExpired || (cert.Status == edge.CertValid && cert.NotAfter.Sub(now) <= soon):
		// Certificates from the local CA renew themselves well before they
		// expire, and only names nobody outside reaches use them.
		if cert.Source == "local" && cert.Status != edge.CertExpired {
			return Item{}, false
		}
		it = certificateItem("certificate:"+cert.Subject, cert.Subject, cert.NotAfter, now)
	default:
		return Item{}, false
	}
	it.Version = version(cert.Status, cert.NotAfter.UTC().Format(time.RFC3339), cert.Error)
	it.Scope = store.ServerScope()
	it.Access = Access{Permission: PermServerRead, Scope: store.ServerScope()}
	// Whoever may read a resource that uses the certificate may see it.
	for _, owner := range cert.Owners {
		if c.Store == nil || owner == edge.PanelOwner {
			continue
		}
		res, err := c.Store.Get(ctx, owner)
		if err != nil {
			continue
		}
		meta := res.Meta
		it.Subject, it.Kind, it.Scope, it.Access = res.ID, res.Kind, res.Scope, Access{Resource: &meta}
		it.Actions = append(it.Actions, Action{Type: ActionOpenResource, Title: "Open", Target: string(res.ID)})
		break
	}
	return it, true
}

// certificateItem describes a certificate that expired or expires soon.
func certificateItem(id, name string, notAfter, now time.Time) Item {
	it := Item{
		ID: id, Category: CategoryCertificate, Scope: store.ServerScope(), Time: now,
		Params: map[string]string{"name": name, "date": notAfter.UTC().Format(time.RFC3339)},
	}
	if !notAfter.After(now) {
		it.Severity, it.Code = events.SeverityError, "certificate_expired"
		it.Title = "The certificate for " + name + " has expired"
		return it
	}
	days := int(notAfter.Sub(now).Hours() / 24)
	it.Severity, it.Code = events.SeverityWarning, "certificate_expiring"
	it.Params["days"] = strconv.Itoa(days)
	it.Title = "The certificate for " + name + " expires " + plural(days, "tomorrow", "in %d days")
	if days == 0 {
		it.Title = "The certificate for " + name + " expires today"
	}
	return it
}

// ModuleState is what suggestions know of a module.
type ModuleState struct {
	ID    string
	Name  string
	State string
}

// Suggestions finds modules that are off and would help, going by what the
// server holds and has. It reads the modules' states for every request, so
// a suggestion goes as soon as its module is turned on.
type Suggestions struct {
	// Modules lists the modules and their states.
	Modules func() []ModuleState
	// Store and Kinds tell whether anything is kept that backups would
	// cover.
	Store store.Reader
	Kinds *store.KindRegistry
	// UnusedDisks counts the disks nothing uses yet, for storage; nil
	// leaves that out.
	UnusedDisks func(ctx context.Context) (int, error)
}

// Items implements Source.
func (s Suggestions) Items(ctx context.Context, q Query) ([]Item, error) {
	states := map[string]ModuleState{}
	busy := false
	for _, m := range s.Modules() {
		states[m.ID] = m
		if m.ID != "network" && (m.State == "on" || m.State == "active") {
			busy = true
		}
	}
	off := func(id string) bool {
		m, ok := states[id]
		return ok && m.State == "off"
	}
	suggest := func(id, code, title, message string, params map[string]string) Item {
		return Item{
			ID: "suggestion:" + id, Category: CategorySuggestion, Severity: events.SeverityInfo, Code: code, Params: params,
			Title: title, Message: message, Time: q.Now, Scope: store.ServerScope(), Version: id,
			Access:  Access{Permission: PermModuleRead, Scope: store.ServerScope()},
			Actions: []Action{{Type: ActionDocs, Title: "About " + states[id].Name, Target: "modules/" + id}},
		}
	}
	var out []Item
	if off("backups") {
		kept, err := s.holdsData(ctx)
		if err != nil {
			return nil, err
		}
		if kept {
			it := suggest("backups", "suggest_backups", "Nothing on this server is backed up",
				"Turn on Backups to keep snapshots and copies of your sites, mail and data, and restore them when something goes wrong.", nil)
			it.Severity = events.SeverityNotice
			out = append(out, it)
		}
	}
	if off("storage") && s.UnusedDisks != nil {
		unused, err := s.UnusedDisks(ctx)
		if err != nil {
			return nil, err
		}
		if unused > 0 {
			out = append(out, suggest("storage", "suggest_storage",
				plural(unused, "A disk isn't used yet", "%d disks aren't used yet"),
				"Turn on Storage to pool them, keep an eye on their health and use them for shares, backups and apps.",
				map[string]string{"count": strconv.Itoa(unused)}))
		}
	}
	if off("monitoring") && busy {
		out = append(out, suggest("monitoring", "suggest_monitoring", "Nothing watches the server's health",
			"Turn on Monitoring for charts of what the server and your sites use, and alerts before a disk fills up or a service stops.", nil))
	}
	return out, nil
}

// holdsData reports whether the server keeps a resource of a module whose
// data backups would cover.
func (s Suggestions) holdsData(ctx context.Context) (bool, error) {
	if s.Store == nil || s.Kinds == nil {
		return false, nil
	}
	for _, k := range s.Kinds.All() {
		info := k.Info()
		if info.Module() == "core" || info.Backup.Exclude {
			continue
		}
		page, err := s.Store.List(ctx, store.Query{Kind: info.Name, Limit: 1})
		if err != nil {
			return false, err
		}
		if len(page.Items) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// UnusedDisks returns a function that counts the server's disks nothing
// uses: not the system's, not removable, and without a partition, a
// filesystem, a mount or a device built on them. It asks the agent at most
// once every keep.
func UnusedDisks(client agent.Client, keep time.Duration) func(ctx context.Context) (int, error) {
	var mu sync.Mutex
	var count int
	var checked time.Time
	return func(ctx context.Context) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		if !checked.IsZero() && time.Since(checked) < keep {
			return count, nil
		}
		disks, err := agent.ListDisks.Call(ctx, client, agent.Empty{}, agent.CallOptions{})
		if err != nil {
			return 0, err
		}
		count = 0
		for _, d := range disks.Disks {
			if !d.System && !d.Removable && d.FSType == "" && len(d.Partitions) == 0 && len(d.Holders) == 0 && len(d.MountPoints) == 0 {
				count++
			}
		}
		checked = time.Now()
		return count, nil
	}
}
