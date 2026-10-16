package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Config is what the client commands are built from.
type Config struct {
	// Document returns the API's OpenAPI document.
	Document func() ([]byte, error)
	// Kind describes a kind, for help and the columns of tables; it may
	// be nil.
	Kind func(name string) (*store.KindInfo, bool)
	// Module returns a module's title, for help; it may be nil.
	Module func(id string) (string, bool)
	// Options are the shared flags' values.
	Options *Options
	// Group is the help group the top-level commands go in.
	Group string
}

// groupAnnotation marks the commands that group others, so a module's
// operations can join a group its kinds made.
const groupAnnotation = "racksail:group"

// AddCommands adds a command for every operation of the document that API
// tokens may use, grouped by what they're about, and the commands that
// sign in, keep profiles and say who you are. It fails when two commands
// would have the same name.
func AddCommands(root *cobra.Command, cfg Config) error {
	raw, err := cfg.Document()
	if err != nil {
		return err
	}
	doc, err := Parse(raw)
	if err != nil {
		return err
	}
	b := &builder{root: root, cfg: cfg, doc: doc}
	for _, op := range doc.Operations {
		if op.Session || handWritten[op.ID] != "" {
			continue
		}
		if err := b.add(op); err != nil {
			return err
		}
	}
	for _, cmd := range []*cobra.Command{newLoginCommand(cfg.Options), newLogoutCommand(cfg.Options), newWhoamiCommand(cfg.Options), newProfileCommand()} {
		if existing := child(root, cmd.Name()); existing != nil {
			return fmt.Errorf("racksail %s is two commands", cmd.Name())
		}
		cmd.GroupID = cfg.Group
		root.AddCommand(cmd)
	}
	return nil
}

type builder struct {
	root *cobra.Command
	cfg  Config
	doc  *Document
}

func (b *builder) add(op *Operation) error {
	words := CommandPath(op)
	parent := b.root
	for i, w := range words[:len(words)-1] {
		g, err := b.group(parent, w, words[:i+1], op)
		if err != nil {
			return err
		}
		parent = g
	}
	name := words[len(words)-1]
	if child(parent, name) != nil {
		return fmt.Errorf("racksail %s is two commands, one of them for %s", strings.Join(words, " "), op.ID)
	}
	cmd := b.command(op, name)
	if parent == b.root {
		cmd.GroupID = b.cfg.Group
	}
	parent.AddCommand(cmd)
	return nil
}

// group returns the command that groups others under a name, making it
// the first time.
func (b *builder) group(parent *cobra.Command, name string, words []string, op *Operation) (*cobra.Command, error) {
	if c := child(parent, name); c != nil {
		if c.Annotations[groupAnnotation] != "true" {
			return nil, fmt.Errorf("racksail %s is a command already, so %s can't have commands under it", strings.Join(words, " "), op.ID)
		}
		return c, nil
	}
	g := &cobra.Command{
		Use:         name,
		Short:       b.groupTitle(words, op),
		Annotations: map[string]string{groupAnnotation: "true"},
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	if parent == b.root {
		g.GroupID = b.cfg.Group
	}
	if alias := pluralAlias(name); alias != "" && child(parent, alias) == nil {
		g.Aliases = []string{alias}
	}
	parent.AddCommand(g)
	return g, nil
}

// pluralAlias lets people type a group's name as a plural too:
// racksail users list as well as racksail user list.
func pluralAlias(name string) string {
	switch {
	case strings.HasSuffix(name, "s"), name == "me", name == "health", name == "metrics":
		return ""
	case strings.HasSuffix(name, "y"):
		return name[:len(name)-1] + "ies"
	}
	return name + "s"
}

func (b *builder) groupTitle(words []string, op *Operation) string {
	name := words[len(words)-1]
	if op.Kind != "" && len(words) == len(CommandPath(op))-1 {
		if info, ok := b.kind(op.Kind); ok {
			return "Manage " + lowerFirst(pluralTitle(info.Title))
		}
	}
	if len(words) == 1 {
		if title, ok := groupTitles[name]; ok {
			return title
		}
		if b.cfg.Module != nil {
			if title, ok := b.cfg.Module(name); ok {
				return title
			}
		}
	}
	if len(op.Tags) > 0 {
		return op.Tags[0]
	}
	return name
}

func (b *builder) kind(name string) (*store.KindInfo, bool) {
	if b.cfg.Kind == nil || name == "" {
		return nil, false
	}
	return b.cfg.Kind(name)
}

func child(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name || slices.Contains(c.Aliases, name) {
			return c
		}
	}
	return nil
}

// opCommand runs one operation.
type opCommand struct {
	doc   *Document
	op    *Operation
	opts  *Options
	flags *flagSet
	in    struct {
		file    flagValue
		sets    flagValue
		replace bool
		all     bool
	}
	// current is the operation that reads what an update replaces.
	current *Operation
	kind    *store.KindInfo
}

func (b *builder) command(op *Operation, name string) *cobra.Command {
	oc := &opCommand{doc: b.doc, op: op, opts: b.cfg.Options, flags: newFlagSet(b.doc)}
	oc.in.file.typ = "file"
	oc.in.sets.typ, oc.in.sets.repeat = "path=value", true
	oc.kind, _ = b.kind(op.Kind)
	if op.Method == http.MethodPut {
		oc.current = b.doc.At(http.MethodGet, op.Path)
	}
	params := op.PathParams()
	use := name
	for _, p := range params {
		use += " <" + p.Name + ">"
	}
	cmd := &cobra.Command{
		Use:               use,
		Short:             op.Summary,
		Long:              oc.long(),
		Args:              cobra.ExactArgs(len(params)),
		ValidArgsFunction: oc.completeArgs,
		RunE:              oc.run,
	}
	if op.Mutating() && !op.Stream {
		Mutating(cmd)
	}
	AddFlags(cmd, oc.opts)
	oc.flags.addParams(op)
	if op.Body != nil {
		oc.flags.addBody(op.Body)
		if oc.kind != nil {
			oc.flags.markSecrets(store.SensitiveFields(oc.kind))
		}
		f := cmd.Flags()
		f.VarP(&oc.in.file, "file", "f", "read the request body from a JSON or YAML file, or - for standard input; flags change it further")
		f.Var(&oc.in.sets, "set", "set a field of the request body, as path=value, such as spec.limits.cpu=200; values are JSON when they parse as JSON")
		if oc.current != nil {
			f.BoolVar(&oc.in.replace, "replace", false, "send only what the flags and --file say, instead of starting from the current settings")
		}
	}
	cmd.Example = oc.example()
	if paged(oc.doc, op) {
		cmd.Flags().BoolVar(&oc.in.all, "all", false, "read every page, not just the first")
	}
	if op.Long || op.DryRun || isJobOperation(op) {
		cmd.Flags().BoolVar(&oc.opts.Detach, "detach", false, "return once the work is queued, instead of following it until it's done")
	}
	oc.flags.register(cmd)
	return cmd
}

// paged recognises lists that come a page at a time: they take a cursor
// and answer with JSON that says where the next page starts.
func paged(doc *Document, op *Operation) bool {
	s, media := op.Success()
	answer := doc.Resolve(s)
	return op.Param("query", "cursor") != nil && media == "application/json" && answer != nil && answer.Properties["next"] != nil
}

// isJobOperation recognises operations that answer with a job to follow.
func isJobOperation(op *Operation) bool {
	s, _ := op.Success()
	return s != nil && strings.TrimPrefix(s.Ref, refPrefix) == "Job" && op.Mutating()
}

// long is a command's help: what the operation does, what it needs and
// how it behaves.
func (oc *opCommand) long() string {
	op := oc.op
	var b strings.Builder
	b.WriteString(op.Summary + ".")
	if op.Description != "" {
		b.WriteString("\n\n" + op.Description)
	}
	var notes []string
	if op.Permission != "" {
		notes = append(notes, "It needs the "+op.Permission+" permission.")
	}
	switch {
	case op.DryRun:
		notes = append(notes, "The change goes through a change set: --dry-run shows what it would do without doing it, and once applied, the command follows the job that carries it out; --detach returns at once.")
	case op.Mutating() && !op.Stream:
		notes = append(notes, "--dry-run shows the request it would send without sending it.")
	}
	if op.Risk == "high" || op.Risk == "critical" {
		notes = append(notes, "Its risk is "+op.Risk+".")
	}
	if op.Destructive {
		notes = append(notes, "It removes data.")
	}
	if op.Stream {
		notes = append(notes, "It goes on until you stop it with Ctrl-C.")
	}
	if oc.current != nil && op.Body != nil {
		notes = append(notes, "It starts from the current settings, so flags only change what they name; --replace sends only what you give.")
	}
	if len(notes) > 0 {
		b.WriteString("\n\n" + strings.Join(notes, " "))
	}
	return b.String()
}

// example writes a command line that uses the operation, with the flags
// its required fields need.
func (oc *opCommand) example() string {
	words := CommandPath(oc.op)
	line := "  racksail " + strings.Join(words, " ")
	for _, p := range oc.op.PathParams() {
		line += " " + placeholder(p)
	}
	// An update starts from the current settings, so changing one field
	// makes the example.
	for _, f := range oc.flags.fields {
		if !f.required {
			continue
		}
		line += " --" + f.flag + " " + exampleValue(oc.doc, f)
		if oc.current != nil {
			break
		}
	}
	return line
}

func placeholder(p *Param) string {
	return strings.ToUpper(strings.ReplaceAll(kebab(p.Name), "-", "_"))
}

func exampleValue(doc *Document, f *field) string {
	s := f.schema
	if f.shape == shapeList {
		if item := doc.Resolve(s.Items); item != nil && len(item.Examples) > 0 {
			return quoteArg(fmt.Sprint(item.Examples[0]))
		}
	}
	if len(s.Examples) > 0 {
		switch ex := s.Examples[0].(type) {
		case []any:
			parts := make([]string, len(ex))
			for i, item := range ex {
				parts[i] = fmt.Sprint(item)
			}
			return quoteArg(strings.Join(parts, ","))
		case string, float64, bool:
			return quoteArg(fmt.Sprint(ex))
		}
	}
	if values := enumOf(doc, f); len(values) > 0 {
		return values[0]
	}
	switch f.shape {
	case shapeScope:
		return "acct_ID"
	case shapeGrant, shapeGrants:
		return "admin"
	case shapeMap:
		return "key=value"
	}
	return strings.ToUpper(strings.ReplaceAll(f.flag, "-", "_"))
}

func quoteArg(s string) string {
	if s == "" || strings.ContainsAny(s, " *?'\"$&|;<>(){}[]") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

// run calls the operation and prints its answer.
func (oc *opCommand) run(cmd *cobra.Command, args []string) error {
	c, err := oc.opts.Connect()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	req, err := oc.request(ctx, c, cmd, args)
	if err != nil {
		return err
	}
	if oc.opts.DryRun && !oc.op.DryRun {
		return describeRequest(out, c, req)
	}
	if oc.op.ID == "streamJob" {
		return oc.watchJob(ctx, c, args[0], cmd)
	}
	if oc.op.Stream {
		return watch(ctx, c, req, out, oc.opts.Output)
	}
	var raw []byte
	if oc.in.all {
		raw, err = oc.allPages(ctx, c, req)
	} else {
		raw, err = oc.call(ctx, c, req, out)
	}
	if err != nil || raw == nil {
		return err
	}
	return oc.answer(ctx, c, cmd, raw)
}

// call sends the request. A JSON answer comes back to print; anything else
// is copied to out as it is, like an audit export.
func (oc *opCommand) call(ctx context.Context, c *Client, req Request, out io.Writer) ([]byte, error) {
	resp, err := c.Do(ctx, req)
	if err != nil {
		var p *Problem
		if errors.As(err, &p) {
			p.flags = oc.flags.byPath
		}
		return nil, err
	}
	defer resp.Body.Close()
	media := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if media != "application/json" && !strings.HasSuffix(media, "+json") {
		_, err := io.Copy(out, resp.Body)
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	return raw, nil
}

// allPages reads every page of a list and joins them into one.
func (oc *opCommand) allPages(ctx context.Context, c *Client, req Request) ([]byte, error) {
	var merged *object
	listKey := ""
	for range 10000 {
		raw, err := oc.call(ctx, c, req, io.Discard)
		if err != nil || raw == nil {
			return nil, err
		}
		v, err := decodeOrdered(raw)
		if err != nil {
			return nil, err
		}
		page, ok := v.(*object)
		if !ok {
			return raw, nil
		}
		if merged == nil {
			merged = page
			for _, k := range page.keys {
				if _, isList := page.values[k].([]any); isList || (page.values[k] == nil && k != "next") {
					listKey = k
					break
				}
			}
		} else if listKey != "" {
			items, _ := merged.get(listKey).([]any)
			more, _ := page.get(listKey).([]any)
			merged.set(listKey, append(items, more...))
		}
		next, _ := page.get("next").(string)
		if next == "" || listKey == "" {
			break
		}
		req.Query.Set("cursor", next)
	}
	if merged.get("next") != nil {
		merged.set("next", "")
	}
	return json.Marshal(merged)
}

// answer prints what the operation answered and follows the work it
// started.
func (oc *opCommand) answer(ctx context.Context, c *Client, cmd *cobra.Command, raw []byte) error {
	out := cmd.OutOrStdout()
	v, err := decodeOrdered(raw)
	if err != nil {
		return fmt.Errorf("unexpected answer from %s: %w", c.Server, err)
	}
	format := oc.opts.Output
	// A change set, alone or with what else the operation made.
	var cs []byte
	switch {
	case isChangeSet(v):
		cs = raw
	case isWrapped(v):
		cs, _ = json.Marshal(v.(*object).get("changeSet"))
	}
	if format != "table" {
		if err := Write(out, raw, format); err != nil {
			return err
		}
	} else if cs != nil {
		if err := printChangeSet(out, cs); err != nil {
			return err
		}
		if !isChangeSet(v) {
			rest := v.(*object)
			others := &object{values: map[string]any{}}
			for _, k := range rest.keys {
				if k != "changeSet" && rest.values[k] != nil {
					others.set(k, rest.values[k])
				}
			}
			if len(others.keys) > 0 {
				fmt.Fprintln(out)
				detailTable(out, others, nil)
			}
		}
	} else if err := printTable(out, v, oc.listKind()); err != nil {
		return err
	}
	if oc.opts.Detach {
		return nil
	}
	if cs != nil {
		return oc.followChangeSet(ctx, c, cmd, cs)
	}
	if isJobOperation(oc.op) {
		var job jobView
		if json.Unmarshal(raw, &job) == nil && job.ID != "" && !job.done() {
			return oc.followJob(ctx, c, cmd, job.ID)
		}
	}
	return nil
}

// isWrapped recognises an answer that carries a change set with other
// things, as opening an account does.
func isWrapped(v any) bool {
	o, ok := v.(*object)
	return ok && isChangeSet(o.get("changeSet"))
}

// Write writes a JSON answer as indented JSON, or as YAML.
func Write(w io.Writer, raw []byte, format string) error {
	if format == "yaml" {
		return writeYAML(w, raw)
	}
	return writeJSON(w, raw)
}

// listKind is the kind whose resources the answer lists, for the columns
// of the table.
func (oc *opCommand) listKind() *store.KindInfo {
	if oc.op.Operation == "list" || oc.op.Operation == "get" {
		return oc.kind
	}
	return nil
}

// followChangeSet follows the job that applies a change set, then shows
// what its actions returned.
func (oc *opCommand) followChangeSet(ctx context.Context, c *Client, cmd *cobra.Command, raw []byte) error {
	var cs changeSet
	if err := json.Unmarshal(raw, &cs); err != nil || cs.DryRun {
		return err
	}
	applied := json.RawMessage(raw)
	if cs.Job != "" && cs.State == "applying" {
		if err := oc.followJob(ctx, c, cmd, cs.Job); err != nil {
			return err
		}
		applied = nil
	}
	hasAction := slices.ContainsFunc(cs.Changes, func(ch change) bool { return ch.Op == "action" })
	if !hasAction || oc.opts.Output != "table" {
		return nil
	}
	if applied == nil {
		if _, err := c.Get(ctx, api.Prefix+"/changesets/"+cs.ID, &applied); err != nil {
			return err
		}
	}
	return printOutputs(cmd.OutOrStdout(), applied, oc.opts.Output)
}

// followJob follows a job until it ends: with its log and a progress line
// for tables, quietly for JSON and YAML, whose answer is printed already.
func (oc *opCommand) followJob(ctx context.Context, c *Client, cmd *cobra.Command, id string) error {
	out := cmd.OutOrStdout()
	quiet := oc.opts.Output != "table"
	status := newStatusLine(cmd.ErrOrStderr())
	if !quiet {
		fmt.Fprintf(out, "Following job %s; Ctrl-C stops following, not the job.\n", id)
	}
	job, err := followJob(ctx, c, id, out, status, quiet)
	if err != nil {
		return err
	}
	if err := jobResult(job); err != nil {
		return err
	}
	if !quiet {
		fmt.Fprintf(out, "Done%s.\n", tookFor(job))
	}
	return nil
}

// watchJob is racksail job watch: the job's log and progress until it
// ends, then the job.
func (oc *opCommand) watchJob(ctx context.Context, c *Client, id string, cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	quiet := oc.opts.Output != "table"
	job, err := followJob(ctx, c, id, out, newStatusLine(cmd.ErrOrStderr()), quiet)
	if err != nil {
		return err
	}
	if quiet {
		if err := Write(out, job.raw, oc.opts.Output); err != nil {
			return err
		}
	} else if job.State == "succeeded" {
		fmt.Fprintf(out, "%s: done%s.\n", job.Title, tookFor(job))
	}
	return jobResult(job)
}
