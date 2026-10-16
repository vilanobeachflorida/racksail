package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// actionWait is how long an operation that runs a short action waits for it
// to finish, so it can answer with the result; longer, it answers 202 and
// the job carries on.
const actionWait = 30 * time.Second

var (
	typeOfChangeSet = reflect.TypeFor[changeset.ChangeSet]()
	typeOfHistory   = reflect.TypeFor[historyBody]()
)

// KindPath returns where the operations of a kind live:
// /api/v1/<module>/<plural>, such as /api/v1/web/sites.
func KindPath(info *store.KindInfo) string {
	return Prefix + "/" + info.Module() + "/" + info.Plural
}

// reservedModules are the first path segments of the core's own
// operations, which no module's kinds may use.
var reservedModules = []string{"health", "version", "openapi.json", "operations", "kinds", "resources", "changesets", "jobs", "events", "audit", "stream", "metrics", "search", "inbox"}

// registerKinds generates the operations of every registered kind.
func (s *Server) registerKinds() {
	reserved := append(slices.Clone(reservedModules), reservedByExtensions()...)
	for _, k := range s.kinds.All() {
		if slices.Contains(reserved, k.Info().Module()) {
			panic(fmt.Sprintf("api: kind %s uses %s, a path the core's own operations need", k.Info().Name, KindPath(k.Info())))
		}
		s.registerKind(k)
	}
}

// tagOf groups a kind's operations under its module's name.
func (s *Server) tagOf(info *store.KindInfo) string {
	if s.opts.Modules != nil {
		if m, ok := s.opts.Modules.Get(info.Module()); ok {
			return m.Manifest().Name
		}
	}
	return camel(info.Plural)
}

// registerKind generates a kind's operations: list, get, create, update,
// delete and history, and one per action. Changes go through the change
// pipeline like any other.
func (s *Server) registerKind(k store.Kind) {
	info := k.Info()
	d := s.kindSchemas(k)
	s.docs[info.Name] = d
	tags := []string{s.tagOf(info)}
	base := KindPath(info)
	title := sentenceTitle(info)
	many := plural(info)
	listID := "list" + camel(info.Module(), info.Plural)
	idParam := pathParam("id", "The "+title+"'s ID, which starts with "+info.Prefix+"_")
	changeSet := s.schemaOf(typeOfChangeSet, "ChangeSet")
	changeResponses := func(applied string) map[string]*huma.Response {
		return map[string]*huma.Response{
			"200": jsonResponse("The change set without applying it, for a dry run", changeSet),
			"202": jsonResponse(applied, changeSet),
		}
	}

	// List.
	op := &huma.Operation{
		OperationID: listID, Method: http.MethodGet, Path: base, Tags: tags, Security: Protected,
		Summary: "List " + many,
		Description: info.Description + " Lists the " + many + " you can see, oldest first unless sort says otherwise, a page at a time: " +
			"follow next for the following page. Filter by owner, labels, phase, search text and the indexed fields.",
		Parameters: append(s.listParams(info), pageParams()...),
		Responses:  map[string]*huma.Response{"200": jsonResponse("A page of "+many, d.list)},
	}
	Describe(op, Info{Kind: info.Name, Operation: "list", Permission: string(ReadPermission(info.Name)), Risk: store.RiskRead})
	s.handle(op, s.listKind(k))

	// Get.
	op = &huma.Operation{
		OperationID: "get" + d.name, Method: http.MethodGet, Path: base + "/{id}", Tags: tags, Security: Protected,
		Summary:     "Get " + withArticle(title),
		Description: "Returns one " + title + ": its settings (spec), and what Racksail last saw of it (status). " + info.Description,
		Parameters:  []*huma.Param{idParam, fieldsParam()},
		Responses:   map[string]*huma.Response{"200": jsonResponse("The "+title, d.resource)},
	}
	Describe(op, Info{Kind: info.Name, Operation: "get", Permission: string(ReadPermission(info.Name)), Risk: store.RiskRead})
	s.handle(op, s.getKind(k))

	// Create.
	op = &huma.Operation{
		OperationID: "create" + d.name, Method: http.MethodPost, Path: base, Tags: tags, Security: Protected,
		Summary: "Create " + withArticle(title),
		Description: info.Description + " Creating one is a change like any other: with dryRun=true the answer is the change set, " +
			"what it would change on the server, without applying it; otherwise the change set is applied and its job carries out the rest.",
		Parameters:  changeParams(),
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/json": {Schema: d.create}}},
		Responses:   changeResponses("The change set, applied to the settings; its job carries out the change on the server"),
	}
	Describe(op, Info{Kind: info.Name, Operation: "create", Permission: string(auth.KindPermission(info.Name, "create")), Risk: info.Risks.Create, DryRun: true, Reversible: true, StepUp: info.Confirm})
	s.handle(op, s.createKind(k))

	// Update.
	op = &huma.Operation{
		OperationID: "update" + d.name, Method: http.MethodPut, Path: base + "/{id}", Tags: tags, Security: Protected,
		Summary: "Change " + withArticle(title),
		Description: "Replaces the " + title + "'s settings with new ones, previewed and applied like any change. " +
			"Send the revision you read to make sure nobody changed it in between; leave labels out to keep them.",
		Parameters:  append([]*huma.Param{idParam}, changeParams()...),
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/json": {Schema: d.edit}}},
		Responses:   changeResponses("The change set, applied to the settings; its job carries out the change on the server"),
	}
	Describe(op, Info{Kind: info.Name, Operation: "update", Permission: string(auth.KindPermission(info.Name, "update")), Risk: info.Risks.Update, DryRun: true, Reversible: true, StepUp: info.Confirm})
	s.handle(op, s.updateKind(k))

	// Delete.
	revision := queryParam("revision", huma.TypeInteger, "The revision you read; when someone changed the "+title+" since, the delete fails with a conflict")
	op = &huma.Operation{
		OperationID: "delete" + d.name, Method: http.MethodDelete, Path: base + "/{id}", Tags: tags, Security: Protected,
		Summary: "Delete " + withArticle(title),
		Description: "Deletes the " + title + " and removes what it set up on the server, previewed and applied like any change. " +
			"Data it removes is kept in a snapshot first where the storage allows it, and the change can be undone.",
		Parameters: append([]*huma.Param{idParam, revision}, changeParams()...),
		Responses:  changeResponses("The change set; its job removes the " + title + " from the server"),
	}
	Describe(op, Info{Kind: info.Name, Operation: "delete", Permission: string(auth.KindPermission(info.Name, "delete")), Risk: info.Risks.Delete, DryRun: true, Destructive: true, Reversible: true, StepUp: info.Confirm})
	s.handle(op, s.deleteKind(k))

	// History.
	op = &huma.Operation{
		OperationID: "get" + d.name + "History", Method: http.MethodGet, Path: base + "/{id}/history", Tags: tags, Security: Protected,
		Summary:     "Show " + withArticle(title) + "'s history",
		Description: "Returns every recorded version of the " + title + "'s settings, oldest first, with the change set that wrote it and who made the change.",
		Parameters:  []*huma.Param{idParam},
		Responses:   map[string]*huma.Response{"200": jsonResponse("The versions", s.schemaOf(typeOfHistory, "History"))},
	}
	Describe(op, Info{Kind: info.Name, Operation: "history", Permission: string(ReadPermission(info.Name)), Risk: store.RiskRead})
	s.handle(op, s.kindHistory(k))

	// Actions.
	for _, a := range info.Actions {
		description := a.Description
		if description == "" {
			description = a.Summary + "."
		}
		if a.Long {
			description += " It runs as a job: the answer is 202 with the change set, whose job reports progress."
		} else {
			description += fmt.Sprintf(" The answer comes when it is done, with what it returned in the change set's output; after %s it is 202 and the job carries on.", actionWait)
		}
		op = &huma.Operation{
			OperationID: lowerFirst(camel(a.Name)) + d.name, Method: http.MethodPost, Path: base + "/{id}/actions/" + a.Name, Tags: tags, Security: Protected,
			Summary:     strings.TrimSuffix(a.Summary, "."),
			Description: description,
			Parameters:  append([]*huma.Param{idParam}, changeParams()...),
			Responses:   changeResponses("The change set; its job carries out the action"),
		}
		if in := d.inputs[a.Name]; in != nil {
			op.RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/json": {Schema: in}}}
		}
		if !a.Long {
			op.Responses["200"].Description = "The change set: of a dry run, or once the action is done, with its output"
		}
		if out := d.outputs[a.Name]; out != nil {
			// A schema object rather than a bare string, so the document
			// keeps the schema it points at.
			op.Extensions = map[string]any{ExtOutput: out}
		}
		Describe(op, Info{
			Kind: info.Name, Operation: a.Name, Permission: string(auth.KindPermission(info.Name, a.Name)), Risk: a.Risk,
			DryRun: true, Long: a.Long, Destructive: a.Destructive, Reversible: a.Reversible, StepUp: info.Confirm && a.Risk != store.RiskLow,
		})
		s.handle(op, s.kindAction(k, a))
	}
}

// fieldsParam documents field selection.
func fieldsParam() *huma.Param {
	return queryParam("fields", huma.TypeString, "Answer with only these fields, as dotted paths separated by commas, such as spec.domains,status.phase; id and kind are always included")
}

// listParams documents the filters of a kind's list.
func (s *Server) listParams(info *store.KindInfo) []*huma.Param {
	sortFields := []string{"id", "createdAt", "updatedAt", "generation", "phase"}
	for _, ix := range info.Indexes {
		sortFields = append(sortFields, ix.Name)
	}
	ps := []*huma.Param{
		queryParam("scope", huma.TypeString, "Only those of one owner: server, reseller:<id> or account:<id>"),
		queryParam("server", huma.TypeString, "Only those placed on this cluster server"),
		listParam("label", "Only those with this label, as key=value; repeat it to require several"),
		listParam("phase", "Only those in one of these phases: pending, applying, ready, degraded, failed or deleting"),
		queryParam("q", huma.TypeString, "Only those whose ID or indexed fields, title or subtitle contain this text"),
		queryParam("sort", huma.TypeString, "The order: "+strings.Join(sortFields, ", ")+", with - in front for the reverse"),
		queryParam("includeDeleted", huma.TypeBoolean, "Also list those being removed"),
		fieldsParam(),
	}
	for _, ix := range info.Indexes {
		what := "Only those whose " + ix.Path + " is this value"
		if ix.Fold {
			what += ", in any case"
		}
		ps = append(ps, queryParam(ix.Name, huma.TypeString, what))
	}
	return ps
}

// listBody is a page of resources.
type listBody struct {
	Items []any  `json:"items"`
	Next  string `json:"next,omitempty"`
}

func (s *Server) listKind(k store.Kind) handler {
	info := k.Info()
	return func(hc huma.Context) (int, any, error) {
		ctx := hc.Context()
		p := paramsOf(hc)
		q := store.Query{Kind: info.Name, Cursor: p.get("cursor"), Sort: p.get("sort"), Search: p.get("q")}
		var err error
		if q.Limit, err = p.limit(); err != nil {
			return 0, nil, err
		}
		if q.Scope, err = p.scope("scope"); err != nil {
			return 0, nil, err
		}
		if server, err := p.id("server"); err != nil {
			return 0, nil, err
		} else if server != "" {
			q.Server = &server
		}
		if q.IncludeDeleted, err = p.boolean("includeDeleted"); err != nil {
			return 0, nil, err
		}
		for _, l := range p.list("label") {
			key, value, ok := strings.Cut(l, "=")
			if !ok || key == "" {
				return 0, nil, badParam("label", "use key=value")
			}
			if q.Labels == nil {
				q.Labels = map[string]string{}
			}
			q.Labels[key] = value
		}
		for _, ph := range p.list("phase") {
			q.Phases = append(q.Phases, store.Phase(ph))
		}
		for _, ix := range info.Indexes {
			if v := p.get(ix.Name); v != "" {
				if q.Index == nil {
					q.Index = map[string]string{}
				}
				q.Index[ix.Name] = v
			}
		}
		fields, err := parseFields(p)
		if err != nil {
			return 0, nil, err
		}
		page, err := List(ctx, s.authorizer(), s.store(), q)
		if err != nil {
			return 0, nil, err
		}
		out := listBody{Items: make([]any, 0, len(page.Items)), Next: page.Next}
		for _, r := range page.Items {
			item, err := fields.apply(r)
			if err != nil {
				return 0, nil, err
			}
			out.Items = append(out.Items, item)
		}
		return http.StatusOK, out, nil
	}
}

// kindID reads the ID of a resource of a kind from a path. An ID that can't
// be one of the kind's is reported as not found.
func kindID(info *store.KindInfo, raw string) (store.ID, error) {
	id, err := store.ParseID(raw)
	if err != nil || id.Prefix() != info.Prefix {
		return "", store.ErrNotFound.New("what", raw)
	}
	return id, nil
}

// readable reads a resource of a kind the signed-in actor may read. Others
// are reported as not found, so other accounts' resources don't reveal that
// they exist.
func (s *Server) readable(ctx context.Context, info *store.KindInfo, raw string) (*store.Resource, error) {
	id, err := kindID(info, raw)
	if err != nil {
		return nil, err
	}
	r, err := s.store().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.Kind != info.Name {
		return nil, store.ErrNotFound.New("what", raw)
	}
	if err := AuthorizeRead(ctx, s.authorizer(), r.Meta); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Server) getKind(k store.Kind) handler {
	info := k.Info()
	return func(hc huma.Context) (int, any, error) {
		fields, err := parseFields(paramsOf(hc))
		if err != nil {
			return 0, nil, err
		}
		r, err := s.readable(hc.Context(), info, hc.Param("id"))
		if err != nil {
			return 0, nil, err
		}
		out, err := fields.apply(r)
		return http.StatusOK, out, err
	}
}

// createBody is the body of a create request.
type createBody struct {
	ID      store.ID          `json:"id"`
	Scope   *store.Scope      `json:"scope"`
	Server  store.ID          `json:"server"`
	Labels  map[string]string `json:"labels"`
	Spec    json.RawMessage   `json:"spec"`
	Secrets map[string]string `json:"secrets"`
}

func (s *Server) createKind(k store.Kind) handler {
	info := k.Info()
	return func(hc huma.Context) (int, any, error) {
		params, err := changeParamsOf(hc)
		if err != nil {
			return 0, nil, err
		}
		var body createBody
		if err := readJSON(hc, &body); err != nil {
			return 0, nil, err
		}
		if len(body.Spec) == 0 {
			return 0, nil, i18n.Invalid(i18n.Field("body.spec", i18n.ErrRequired))
		}
		scope := store.ServerScope()
		switch {
		case info.Scope != store.ScopeServer || len(info.Levels) > 0:
			if body.Scope == nil {
				if info.Scope != store.ScopeServer {
					return 0, nil, i18n.Invalid(i18n.Field("body.scope", i18n.ErrRequired))
				}
				break
			}
			scope = *body.Scope
			if scope.Level == "" {
				scope.Level = info.Scope
			}
		case body.Scope != nil && *body.Scope != scope:
			return 0, nil, i18n.Invalid(i18n.Field("body.scope", i18n.ErrBadValue, "reason", info.Title+" resources belong to the server"))
		}
		return s.submit(hc.Context(), params, changeset.Request{Changes: []changeset.Change{{
			Op: changeset.OpCreate, Kind: info.Name, ID: body.ID, Scope: scope, Server: body.Server,
			Labels: body.Labels, Spec: body.Spec, Secrets: body.Secrets,
		}}}, false)
	}
}

// updateBody is the body of an update request.
type updateBody struct {
	Spec     json.RawMessage   `json:"spec"`
	Labels   map[string]string `json:"labels"`
	Secrets  map[string]string `json:"secrets"`
	Revision int64             `json:"revision"`
}

// Updates, deletes and actions don't read the resource first: the change
// pipeline looks up the request's idempotency key before it reads anything,
// so a retry gets the first answer even once the resource is gone, as after
// a delete, and it answers for a resource the caller may not read as a read
// would.

func (s *Server) updateKind(k store.Kind) handler {
	info := k.Info()
	return func(hc huma.Context) (int, any, error) {
		params, err := changeParamsOf(hc)
		if err != nil {
			return 0, nil, err
		}
		id, err := kindID(info, hc.Param("id"))
		if err != nil {
			return 0, nil, err
		}
		var body updateBody
		if err := readJSON(hc, &body); err != nil {
			return 0, nil, err
		}
		if len(body.Spec) == 0 {
			return 0, nil, i18n.Invalid(i18n.Field("body.spec", i18n.ErrRequired))
		}
		return s.submit(hc.Context(), params, changeset.Request{Changes: []changeset.Change{{
			Op: changeset.OpUpdate, Kind: info.Name, ID: id, Spec: body.Spec, Labels: body.Labels,
			Secrets: body.Secrets, Revision: body.Revision,
		}}}, false)
	}
}

func (s *Server) deleteKind(k store.Kind) handler {
	info := k.Info()
	return func(hc huma.Context) (int, any, error) {
		params, err := changeParamsOf(hc)
		if err != nil {
			return 0, nil, err
		}
		revision, err := paramsOf(hc).integer("revision", 0, 0, int(^uint(0)>>1))
		if err != nil {
			return 0, nil, err
		}
		id, err := kindID(info, hc.Param("id"))
		if err != nil {
			return 0, nil, err
		}
		return s.submit(hc.Context(), params, changeset.Request{Changes: []changeset.Change{{
			Op: changeset.OpDelete, Kind: info.Name, ID: id, Revision: int64(revision),
		}}}, false)
	}
}

func (s *Server) kindAction(k store.Kind, a store.Action) handler {
	info := k.Info()
	return func(hc huma.Context) (int, any, error) {
		params, err := changeParamsOf(hc)
		if err != nil {
			return 0, nil, err
		}
		id, err := kindID(info, hc.Param("id"))
		if err != nil {
			return 0, nil, err
		}
		input, err := readRaw(hc)
		if err != nil {
			return 0, nil, err
		}
		return s.submit(hc.Context(), params, changeset.Request{Changes: []changeset.Change{{
			Op: changeset.OpAction, Kind: info.Name, ID: id, Action: a.Name, Input: input,
		}}}, !a.Long)
	}
}

// submit sends a request through the change pipeline for the signed-in
// actor. A short action that was applied is waited for, so the answer has
// its output.
func (s *Server) submit(ctx context.Context, params ChangeParams, req changeset.Request, wait bool) (int, any, error) {
	pipeline := s.pipeline()
	if pipeline == nil {
		return 0, nil, i18n.ErrUnavailable.New("what", "The change pipeline")
	}
	out, err := Submit(ctx, pipeline, params, req)
	if err != nil {
		return 0, nil, err
	}
	if wait && !out.Body.DryRun && out.Body.Job != "" && s.awaitJob(ctx, out.Body.Job) {
		if cs, err := pipeline.Get(ctx, out.Body.ID); err == nil {
			return http.StatusOK, cs, nil
		}
	}
	return out.Status, out.Body, nil
}

// awaitJob waits for a job to finish, up to actionWait, and reports
// whether it finished in time. The server closing ends the wait, and the
// answer is 202 with the change set, whose job carries on.
func (s *Server) awaitJob(ctx context.Context, id store.ID) bool {
	q := s.jobs()
	if q == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, actionWait)
	defer cancel()
	defer context.AfterFunc(s.closing, cancel)()
	_, err := q.Wait(ctx, id)
	return err == nil
}

// historyBody is a resource's recorded versions.
type historyBody struct {
	Versions []store.Version `json:"versions"`
}

func (s *Server) kindHistory(k store.Kind) handler {
	info := k.Info()
	return func(hc huma.Context) (int, any, error) {
		r, err := s.readable(hc.Context(), info, hc.Param("id"))
		if err != nil {
			return 0, nil, err
		}
		versions, err := s.store().History(hc.Context(), r.ID)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, historyBody{Versions: versions}, nil
	}
}

// KindDescription describes a kind for clients that build screens and
// commands from it.
type KindDescription struct {
	Name        string           `json:"name" doc:"<module>.<name>"`
	Module      string           `json:"module"`
	Title       string           `json:"title"`
	Plural      string           `json:"plural"`
	Description string           `json:"description"`
	Prefix      string           `json:"prefix" doc:"What its IDs start with, before the underscore"`
	Scope       store.ScopeLevel `json:"scope" enum:"server,reseller,account" doc:"Who owns resources of the kind"`
	// Levels are where else its resources may belong.
	Levels    []store.ScopeLevel `json:"levels,omitempty" doc:"Further levels its resources may belong to, as users belong to the server, a reseller or an account"`
	Path      string             `json:"path" doc:"Where its operations are"`
	Singleton bool               `json:"singleton,omitempty" doc:"At most one per owner"`
	Confirm   bool               `json:"confirm,omitempty" doc:"Whether changing one asks people in the panel to confirm it's them"`
	Risks     KindRisks          `json:"risks"`
	Actions   []ActionInfo       `json:"actions"`
	Indexes   []IndexInfo        `json:"indexes"`
	Events    []string           `json:"events" doc:"The event types the kind emits"`
	UI        store.UIHints      `json:"ui"`
	Backup    BackupInfo         `json:"backup"`
	// Schemas are filled in when one kind is asked for.
	Spec    json.RawMessage            `json:"spec,omitempty" doc:"The JSON Schema of the settings"`
	Status  json.RawMessage            `json:"status,omitempty" doc:"The JSON Schema of the status"`
	Inputs  map[string]json.RawMessage `json:"inputs,omitempty" doc:"The JSON Schemas of the actions' inputs, by action"`
	Outputs map[string]json.RawMessage `json:"outputs,omitempty" doc:"The JSON Schemas of the actions' outputs, by action"`
}

// KindRisks are the risks of a kind's operations.
type KindRisks struct {
	Create store.Risk `json:"create"`
	Update store.Risk `json:"update"`
	Delete store.Risk `json:"delete"`
}

// ActionInfo describes an action.
type ActionInfo struct {
	Name        string     `json:"name"`
	Title       string     `json:"title,omitempty"`
	Summary     string     `json:"summary"`
	Description string     `json:"description,omitempty"`
	Risk        store.Risk `json:"risk" enum:"read,low,medium,high,critical"`
	Event       string     `json:"event" doc:"The event it emits once it is done"`
	Long        bool       `json:"long,omitempty"`
	Destructive bool       `json:"destructive,omitempty"`
	Reversible  bool       `json:"reversible,omitempty"`
	HasInput    bool       `json:"hasInput,omitempty"`
}

// IndexInfo describes an index, which lists filter and sort on.
type IndexInfo struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Unique bool   `json:"unique,omitempty"`
}

// BackupInfo says whether backups cover a kind.
type BackupInfo struct {
	Covered bool   `json:"covered"`
	Reason  string `json:"reason,omitempty" doc:"Why the kind isn't backed up"`
}

func describeKind(k store.Kind) KindDescription {
	info := k.Info()
	d := KindDescription{
		Name: info.Name, Module: info.Module(), Title: info.Title, Plural: info.Plural, Description: info.Description,
		Prefix: info.Prefix, Scope: info.Scope, Levels: info.Levels, Path: KindPath(info), Singleton: info.Singleton, Confirm: info.Confirm, UI: info.UI,
		Risks:   KindRisks{Create: info.Risks.Create, Update: info.Risks.Update, Delete: info.Risks.Delete},
		Backup:  BackupInfo{Covered: !info.Backup.Exclude, Reason: info.Backup.Reason},
		Actions: []ActionInfo{}, Indexes: []IndexInfo{}, Events: []string{},
	}
	for _, a := range info.Actions {
		d.Actions = append(d.Actions, ActionInfo{
			Name: a.Name, Title: a.Title, Summary: a.Summary, Description: a.Description, Risk: a.Risk, Event: a.EventName(info.Name),
			Long: a.Long, Destructive: a.Destructive, Reversible: a.Reversible, HasInput: a.Input != nil,
		})
	}
	for _, ix := range info.Indexes {
		d.Indexes = append(d.Indexes, IndexInfo{Name: ix.Name, Path: ix.Path, Unique: ix.Unique})
	}
	for _, t := range events.KindTypes(k) {
		d.Events = append(d.Events, t.Name)
	}
	return d
}

type kindsOutput struct {
	Body struct {
		Kinds []KindDescription `json:"kinds"`
	}
}

type kindOutput struct {
	Body KindDescription
}

type kindInput struct {
	Name string `path:"name" doc:"The kind's name, such as web.site"`
}

// registerKindDocs adds the operations that describe kinds.
func (s *Server) registerKindDocs() {
	op := huma.Operation{
		OperationID: "listKinds", Method: http.MethodGet, Path: Prefix + "/kinds", Tags: []string{"System"}, Security: Protected,
		Summary:     "List the kinds of resources",
		Description: "Describes every kind of resource this panel manages: its operations, actions with their risks, indexes, events and the hints screens are built from.",
	}
	Describe(&op, Info{Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, _ *struct{}) (*kindsOutput, error) {
		if _, err := Actor(ctx); err != nil {
			return nil, err
		}
		out := &kindsOutput{}
		out.Body.Kinds = []KindDescription{}
		for _, k := range s.kinds.All() {
			out.Body.Kinds = append(out.Body.Kinds, describeKind(k))
		}
		return out, nil
	})
	op = huma.Operation{
		OperationID: "getKind", Method: http.MethodGet, Path: Prefix + "/kinds/{name}", Tags: []string{"System"}, Security: Protected,
		Summary:     "Describe a kind of resource",
		Description: "Describes one kind of resource, with the JSON Schemas of its settings, its status and its actions' inputs and outputs, so forms can be built from them.",
	}
	Describe(&op, Info{Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *kindInput) (*kindOutput, error) {
		if _, err := Actor(ctx); err != nil {
			return nil, err
		}
		k, ok := s.kinds.Get(in.Name)
		if !ok {
			return nil, i18n.ErrNotFound.New("what", "The kind "+in.Name)
		}
		d := describeKind(k)
		docs := s.docs[k.Info().Name]
		var err error
		if d.Spec, err = s.standalone(docs.spec); err != nil {
			return nil, err
		}
		if d.Status, err = s.standalone(docs.status); err != nil {
			return nil, err
		}
		for name, sch := range docs.inputs {
			if d.Inputs == nil {
				d.Inputs = map[string]json.RawMessage{}
			}
			if d.Inputs[name], err = s.standalone(sch); err != nil {
				return nil, err
			}
		}
		for name, sch := range docs.outputs {
			if d.Outputs == nil {
				d.Outputs = map[string]json.RawMessage{}
			}
			if d.Outputs[name], err = s.standalone(sch); err != nil {
				return nil, err
			}
		}
		return &kindOutput{Body: d}, nil
	})
}
