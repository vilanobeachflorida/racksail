package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// shape is how what a flag is given becomes part of a request.
type shape int

const (
	// A string, number or boolean.
	shapeScalar shape = iota
	// A list of those, comma-separated or given several times.
	shapeList
	// key=value pairs, given several times.
	shapeMap
	// An owner: server, account:ID, reseller:ID or an account's ID.
	shapeScope
	// A role and where it applies: ROLE or ROLE@SCOPE, or a JSON object.
	shapeGrant
	// Several of those.
	shapeGrants
	// Any JSON value.
	shapeJSON
	// A list of JSON values, one per flag.
	shapeJSONList
	// An object whose own fields get flags.
	shapeObject
)

// field is one flag that sets part of a request: a query or header
// parameter, or a field of the body.
type field struct {
	flag string
	// in is query, header or body; path is the parameter's name, or where
	// the field is in the body.
	in       string
	path     []string
	schema   *Schema
	shape    shape
	required bool
	// secret fields of a kind's spec are sent in the request's secrets,
	// which are kept apart from the settings.
	secret bool
	// password fields are read from a file or standard input, never from
	// the command line, where shells keep them and other users see them.
	password bool
	value    *flagValue
}

// flagValue keeps what a flag was given until the command runs, when its
// schema says what it means.
type flagValue struct {
	values []string
	typ    string
	repeat bool
	split  bool
}

func (v *flagValue) String() string { return strings.Join(v.values, ",") }

func (v *flagValue) Set(s string) error {
	if !v.repeat {
		v.values = []string{s}
		return nil
	}
	if v.split {
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				v.values = append(v.values, part)
			}
		}
		return nil
	}
	v.values = append(v.values, s)
	return nil
}

func (v *flagValue) Type() string { return v.typ }

// IsBoolFlag lets boolean flags go without a value.
func (v *flagValue) IsBoolFlag() bool { return v.typ == "bool" }

// globalFlags are the names the shared and per-command flags take, which
// fields of requests can't.
var globalFlags = []string{"profile", "server", "ca-file", "output", "dry-run", "detach", "all", "file", "set", "replace", "yes", "help"}

// renamed are flags for fields whose own names other flags take: the
// cluster server a resource goes on can't be --server, the panel's address.
var renamed = map[string]string{"server": "on-server", "q": "search"}

// flagSet is the flags of one operation's command.
type flagSet struct {
	doc    *Document
	fields []*field
	used   map[string]bool
	// byPath names the flag that sets each body field, for messages.
	byPath map[string]string
}

func newFlagSet(doc *Document) *flagSet {
	fs := &flagSet{doc: doc, used: map[string]bool{}, byPath: map[string]string{}}
	for _, name := range globalFlags {
		fs.used[name] = true
	}
	return fs
}

// claim takes the first free name of the candidates.
func (fs *flagSet) claim(candidates ...string) (string, bool) {
	for _, c := range candidates {
		if c != "" && !fs.used[c] {
			fs.used[c] = true
			return c, true
		}
	}
	return "", false
}

// addParams adds a flag for every query and header parameter but dryRun,
// which --dry-run sets.
func (fs *flagSet) addParams(op *Operation) {
	for _, p := range op.Params {
		if (p.In != "query" && p.In != "header") || (p.In == "query" && p.Name == "dryRun") {
			continue
		}
		name := kebab(p.Name)
		if p.In == "header" {
			name = strings.ToLower(p.Name)
		}
		if r, ok := renamed[name]; ok {
			name = r
		}
		flag, ok := fs.claim(name)
		if !ok {
			continue
		}
		s := fs.doc.Resolve(p.Schema)
		if s == nil {
			s = &Schema{Types: Types{"string"}}
		}
		f := &field{flag: flag, in: p.In, path: []string{p.Name}, schema: s, shape: shapeScalar}
		if s.Types.Is("array") {
			f.shape = shapeList
		}
		if p.Description != "" && s.Description == "" {
			s = &Schema{Types: s.Types, Format: s.Format, Enum: s.Enum, Items: s.Items, Description: p.Description}
			f.schema = s
		}
		f.value = newFlagValue(fs.doc, f)
		fs.fields = append(fs.fields, f)
	}
}

// addBody adds flags for the fields of a request body: its own fields, a
// kind's spec fields as if they were its own, and the fields of objects
// inside those, joined with dashes, as --limits-memory-max. What can't be
// a flag is still reachable with --set and --file.
func (fs *flagSet) addBody(body *Schema) {
	s := fs.doc.Resolve(body)
	if s == nil || len(s.Properties) == 0 {
		return
	}
	// The spec's fields come first, so they get the plain names.
	spec := fs.doc.Resolve(s.Properties["spec"])
	flattened := spec != nil && len(spec.Properties) > 0
	if flattened {
		fs.walk(spec, []string{"spec"}, "", s.IsRequired("spec"), 0)
	}
	for _, name := range sortedKeys(s.Properties) {
		if name == "spec" && flattened {
			continue
		}
		fs.addField(s, name, nil, "", true, 0)
	}
}

func (fs *flagSet) walk(obj *Schema, path []string, prefix string, required bool, depth int) {
	for _, name := range sortedKeys(obj.Properties) {
		fs.addField(obj, name, path, prefix, required, depth)
	}
}

func (fs *flagSet) addField(parent *Schema, name string, path []string, prefix string, parentRequired bool, depth int) {
	prop := parent.Properties[name]
	s := fs.doc.Resolve(prop)
	if s == nil || s.ReadOnly {
		return
	}
	if prop.Description != "" && s.Description != prop.Description {
		copied := *s
		copied.Description = prop.Description
		s = &copied
	}
	full := append(slices.Clone(path), name)
	sh := shapeOf(fs.doc, s)
	if sh == shapeObject {
		if depth < 2 {
			fs.walk(s, full, prefix+kebab(name)+"-", false, depth+1)
		}
		return
	}
	plain := prefix + kebab(name)
	candidates := []string{plain}
	switch {
	case len(path) == 0:
		if r, ok := renamed[plain]; ok {
			candidates = []string{r}
		}
		candidates = append(candidates, "body-"+plain)
	case len(path) == 1 && path[0] == "spec" && prefix == "":
		candidates = append(candidates, "spec-"+plain)
	}
	flag, ok := fs.claim(candidates...)
	if !ok {
		return
	}
	f := &field{flag: flag, in: "body", path: full, schema: s, shape: sh, required: parentRequired && parent.IsRequired(name)}
	if len(path) == 0 {
		f.required = parent.IsRequired(name)
	}
	f.password = sh == shapeScalar && s.Format == "password"
	f.value = newFlagValue(fs.doc, f)
	fs.fields = append(fs.fields, f)
	fs.byPath[strings.Join(full, ".")] = flag
}

func newFlagValue(doc *Document, f *field) *flagValue {
	v := &flagValue{typ: "string"}
	switch f.shape {
	case shapeScalar:
		switch f.schema.Types.Main() {
		case "boolean":
			v.typ = "bool"
		case "integer":
			v.typ = "int"
			if isBytes(f.schema) {
				v.typ = "size"
			}
		case "number":
			v.typ = "number"
		}
	case shapeList:
		v.typ, v.repeat, v.split = "strings", true, true
		if item := doc.Resolve(f.schema.Items); item != nil && item.Types.Main() == "integer" {
			v.typ = "ints"
		}
	case shapeMap:
		v.typ, v.repeat = "key=value", true
	case shapeScope:
		v.typ = "owner"
	case shapeGrant:
		v.typ = "grant"
	case shapeGrants:
		v.typ, v.repeat = "grant", true
	case shapeJSON:
		v.typ = "json"
	case shapeJSONList:
		v.typ, v.repeat = "json", true
	}
	return v
}

// markSecrets marks the flags of a kind's secret spec fields, whose values
// go in the request's secrets.
func (fs *flagSet) markSecrets(paths []string) {
	for _, f := range fs.fields {
		if f.in == "body" && len(f.path) > 1 && f.path[0] == "spec" && f.shape == shapeScalar &&
			slices.Contains(paths, strings.Join(f.path[1:], ".")) {
			f.secret = true
		}
	}
}

// register adds the flags to a command, with their help.
func (fs *flagSet) register(cmd *cobra.Command) {
	for _, f := range fs.fields {
		cmd.Flags().Var(f.value, f.flag, fs.usage(f))
		if f.value.typ == "bool" {
			cmd.Flags().Lookup(f.flag).NoOptDefVal = "true"
		}
		if values := enumOf(fs.doc, f); len(values) > 0 {
			cmd.RegisterFlagCompletionFunc(f.flag, cobra.FixedCompletions(values, cobra.ShellCompDirectiveNoFileComp))
		}
	}
}

func (fs *flagSet) usage(f *field) string {
	text := strings.TrimSuffix(firstSentences(f.schema.Description), ".")
	if text == "" {
		text = strings.ReplaceAll(kebab(f.path[len(f.path)-1]), "-", " ")
	}
	var notes []string
	if values := enumOf(fs.doc, f); len(values) > 0 {
		notes = append(notes, "one of "+strings.Join(values, ", "))
	}
	switch f.shape {
	case shapeScope:
		notes = append(notes, "server, account:ID, reseller:ID or an account's ID")
	case shapeGrant, shapeGrants:
		notes = append(notes, "ROLE or ROLE@OWNER, such as admin or owner@acct_123, or a JSON object")
	case shapeMap:
		if f.path[len(f.path)-1] == "secrets" {
			notes = append(notes, "key=@file reads the value from a file and key=- from standard input")
		}
	case shapeScalar:
		if f.value.typ == "size" {
			notes = append(notes, "bytes, or a size such as 512M or 2G")
		}
		if f.secret {
			notes = append(notes, "kept as a secret; @file reads it from a file and - from standard input")
		}
		if f.password {
			notes = append(notes, "- asks for it, or reads it from standard input, and @file reads it from a file")
		}
	}
	if f.required {
		notes = append(notes, "required")
	}
	if len(notes) > 0 {
		text += " (" + strings.Join(notes, "; ") + ")"
	}
	return text
}

// firstSentences shortens a description for a flag's help line.
func firstSentences(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 160 {
		return s
	}
	if i := strings.Index(s, ". "); i > 0 && i < 200 {
		return s[:i+1]
	}
	return s
}

func enumOf(doc *Document, f *field) []string {
	s := f.schema
	if f.shape == shapeList {
		s = doc.Resolve(s.Items)
	}
	if s == nil {
		return nil
	}
	var out []string
	for _, e := range s.Enum {
		if text, ok := e.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func isBytes(s *Schema) bool {
	return s.Types.Main() == "integer" && strings.Contains(strings.ToLower(s.Description), "bytes")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// shapeOf works out how a field is given on the command line.
func shapeOf(doc *Document, s *Schema) shape {
	switch {
	case isScope(doc, s):
		return shapeScope
	case isGrant(doc, s):
		return shapeGrant
	case s.Types.Main() == "array":
		item := doc.Resolve(s.Items)
		switch {
		case item == nil:
			return shapeJSONList
		case isScalar(item):
			return shapeList
		case isGrant(doc, item):
			return shapeGrants
		}
		return shapeJSONList
	case s.Types.Main() == "object" || (s.Types.Main() == "" && len(s.Properties) > 0):
		if len(s.Properties) > 0 {
			return shapeObject
		}
		if v := doc.Resolve(s.AdditionalProperties); v != nil && isScalar(v) {
			return shapeMap
		}
		return shapeJSON
	case isScalar(s):
		return shapeScalar
	}
	return shapeJSON
}

func isScalar(s *Schema) bool {
	switch s.Types.Main() {
	case "string", "integer", "number", "boolean":
		return true
	}
	return false
}

// isScope recognises the owner of a resource: a level and an ID.
func isScope(doc *Document, s *Schema) bool {
	if s == nil || len(s.Properties) != 2 {
		return false
	}
	level := doc.Resolve(s.Properties["level"])
	return level != nil && s.Properties["id"] != nil && slices.Contains(level.Enum, any("server"))
}

// isGrant recognises a grant: a role, and the owner where it applies.
func isGrant(doc *Document, s *Schema) bool {
	if s == nil || s.Properties["role"] == nil || s.Properties["scope"] == nil {
		return false
	}
	return isScope(doc, doc.Resolve(s.Properties["scope"])) && len(doc.Resolve(s.Properties["role"]).Enum) > 0
}

// given reports whether the flag was used.
func (f *field) given(cmd *cobra.Command) bool { return cmd.Flags().Changed(f.flag) }

// json converts what the flag was given into the JSON value it stands for.
func (f *field) json(doc *Document, stdin io.Reader) (any, error) {
	vals := f.value.values
	switch f.shape {
	case shapeScalar:
		if len(vals) == 0 {
			return nil, nil
		}
		return f.scalar(f.schema, vals[0])
	case shapeList:
		item := doc.Resolve(f.schema.Items)
		out := make([]any, 0, len(vals))
		for _, v := range vals {
			x, err := f.scalar(item, v)
			if err != nil {
				return nil, err
			}
			out = append(out, x)
		}
		return out, nil
	case shapeMap:
		out := map[string]any{}
		item := doc.Resolve(f.schema.AdditionalProperties)
		for _, v := range vals {
			key, text, ok := strings.Cut(v, "=")
			if !ok || key == "" {
				return nil, fmt.Errorf("--%s takes key=value, not %q", f.flag, v)
			}
			if f.path[len(f.path)-1] == "secrets" {
				var err error
				if text, err = readSecret(text, stdin); err != nil {
					return nil, fmt.Errorf("--%s %s: %w", f.flag, key, err)
				}
			}
			x, err := f.scalar(item, text)
			if err != nil {
				return nil, err
			}
			out[key] = x
		}
		return out, nil
	case shapeScope:
		if len(vals) == 0 {
			return nil, nil
		}
		return parseScope(f.flag, vals[0])
	case shapeGrant:
		if len(vals) == 0 {
			return nil, nil
		}
		return parseGrant(doc, f.flag, f.schema, vals[0])
	case shapeGrants:
		out := make([]any, 0, len(vals))
		for _, v := range vals {
			g, err := parseGrant(doc, f.flag, doc.Resolve(f.schema.Items), v)
			if err != nil {
				return nil, err
			}
			out = append(out, g)
		}
		return out, nil
	case shapeJSON:
		if len(vals) == 0 {
			return nil, nil
		}
		return looseJSON(vals[0]), nil
	case shapeJSONList:
		out := make([]any, 0, len(vals))
		for _, v := range vals {
			out = append(out, looseJSON(v))
		}
		return out, nil
	}
	return nil, nil
}

// scalar converts one value by its schema, checking it against the
// schema's values when it lists them.
func (f *field) scalar(s *Schema, text string) (any, error) {
	if s == nil {
		return text, nil
	}
	switch s.Types.Main() {
	case "boolean":
		b, err := strconv.ParseBool(text)
		if err != nil {
			return nil, fmt.Errorf("--%s is true or false, not %q", f.flag, text)
		}
		return b, nil
	case "integer":
		if isBytes(s) {
			n, err := parseSize(text)
			if err != nil {
				return nil, fmt.Errorf("--%s: %w", f.flag, err)
			}
			return n, nil
		}
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("--%s takes a whole number, not %q", f.flag, text)
		}
		return n, nil
	case "number":
		n, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("--%s takes a number, not %q", f.flag, text)
		}
		return n, nil
	}
	if len(s.Enum) > 0 && !slices.Contains(s.Enum, any(text)) {
		var values []string
		for _, e := range s.Enum {
			values = append(values, fmt.Sprint(e))
		}
		return nil, fmt.Errorf("--%s is one of %s, not %q", f.flag, strings.Join(values, ", "), text)
	}
	return text, nil
}

var sizePattern = regexp.MustCompile(`^(?i)\s*([0-9]+(?:\.[0-9]+)?)\s*([kmgtp]?)(i?b?)\s*$`)

// parseSize reads a number of bytes, or a size as systemd writes them:
// K, M, G, T and P count in 1024s, and KB, MB, GB, TB and PB in 1000s.
func parseSize(text string) (int64, error) {
	m := sizePattern.FindStringSubmatch(text)
	if m == nil {
		return 0, fmt.Errorf("%q isn't a size; use bytes, or a size such as 512M or 2G", text)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	unit := strings.ToUpper(m[2])
	base := 1024.0
	if strings.EqualFold(m[3], "b") && unit != "" {
		base = 1000
	}
	if exp := strings.Index("KMGTP", unit); unit != "" && exp >= 0 {
		n *= math.Pow(base, float64(exp+1))
	}
	if n > math.MaxInt64 {
		return 0, fmt.Errorf("%q is too large", text)
	}
	return int64(n), nil
}

// readSecret reads a secret's value from a file for @path, or from
// standard input for -, so it stays out of shell history.
func readSecret(text string, stdin io.Reader) (string, error) {
	switch {
	case text == "-":
		raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
		return strings.TrimRight(string(raw), "\r\n"), err
	case strings.HasPrefix(text, "@"):
		raw, err := os.ReadFile(text[1:])
		return strings.TrimRight(string(raw), "\r\n"), err
	}
	return text, nil
}

// parseScope reads an owner: server, account:ID, reseller:ID, or an
// account's ID on its own.
func parseScope(flag, text string) (map[string]any, error) {
	text = strings.TrimSpace(text)
	if text == "server" {
		return map[string]any{"level": "server"}, nil
	}
	if level, id, ok := strings.Cut(text, ":"); ok && (level == "account" || level == "reseller") && id != "" {
		return map[string]any{"level": level, "id": id}, nil
	}
	if strings.HasPrefix(text, "acct_") {
		return map[string]any{"level": "account", "id": text}, nil
	}
	return nil, fmt.Errorf("--%s is server, account:ID, reseller:ID or an account's ID, not %q", flag, text)
}

// parseGrant reads a grant: ROLE, ROLE@OWNER, or the grant as JSON.
func parseGrant(doc *Document, flag string, s *Schema, text string) (any, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "{") {
		var v any
		if err := json.Unmarshal([]byte(text), &v); err != nil {
			return nil, fmt.Errorf("--%s: %w", flag, err)
		}
		return v, nil
	}
	role, owner, hasOwner := strings.Cut(text, "@")
	if roles := doc.Resolve(s.Properties["role"]); roles != nil && !slices.Contains(roles.Enum, any(role)) {
		var names []string
		for _, e := range roles.Enum {
			names = append(names, fmt.Sprint(e))
		}
		return nil, fmt.Errorf("--%s: the role is one of %s, not %q", flag, strings.Join(names, ", "), role)
	}
	scope := map[string]any{"level": "server"}
	if hasOwner {
		var err error
		if scope, err = parseScope(flag, owner); err != nil {
			return nil, err
		}
	}
	return map[string]any{"role": role, "scope": scope}, nil
}

// looseJSON reads a value as JSON, and as a string when it isn't JSON.
func looseJSON(text string) any {
	var v any
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil || dec.More() {
		return text
	}
	return v
}
