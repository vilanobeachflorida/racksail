package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

// request makes the request for an operation from the command's arguments
// and flags.
func (oc *opCommand) request(ctx context.Context, c *Client, cmd *cobra.Command, args []string) (Request, error) {
	op := oc.op
	path := op.Path
	for i, p := range op.PathParams() {
		path = strings.Replace(path, "{"+p.Name+"}", url.PathEscape(args[i]), 1)
	}
	req := Request{Method: op.Method, Path: path, Query: url.Values{}, Header: http.Header{}}
	if op.DryRun && oc.opts.DryRun {
		req.Query.Set("dryRun", "true")
	}
	for _, f := range oc.flags.fields {
		if f.in == "body" || !f.given(cmd) {
			continue
		}
		v, err := f.json(oc.doc, cmd.InOrStdin())
		if err != nil {
			return req, err
		}
		values, ok := v.([]any)
		if !ok {
			values = []any{v}
		}
		for _, item := range values {
			if f.in == "header" {
				req.Header.Add(f.path[0], fmt.Sprint(item))
			} else {
				req.Query.Add(f.path[0], fmt.Sprint(item))
			}
		}
	}
	if op.Body != nil {
		body, err := oc.body(ctx, c, cmd, path)
		if err != nil {
			return req, err
		}
		req.Body = body
	}
	if _, media := op.Success(); media != "" && media != "application/json" {
		req.Accept = media
	}
	return req, nil
}

// body makes the request body: what --file holds, or for a change to
// something that already exists, what it is now; then what the flags and
// --set say on top.
func (oc *opCommand) body(ctx context.Context, c *Client, cmd *cobra.Command, path string) (map[string]any, error) {
	doc := map[string]any{}
	switch {
	case oc.in.file.String() != "":
		name := oc.in.file.String()
		v, err := readDocument(name, cmd.InOrStdin())
		if err != nil {
			return nil, err
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s doesn't hold an object", name)
		}
		doc = m
	case oc.current != nil && !oc.in.replace:
		current, err := oc.currentBody(ctx, c, path)
		if err != nil {
			return nil, err
		}
		doc = current
	}
	for _, f := range oc.flags.fields {
		if f.in != "body" || !f.given(cmd) {
			continue
		}
		v, err := f.json(oc.doc, cmd.InOrStdin())
		if err != nil {
			return nil, err
		}
		where := f.path
		if f.secret {
			// Secret fields' values go apart from the settings.
			text, err := readSecret(fmt.Sprint(v), cmd.InOrStdin())
			if err != nil {
				return nil, fmt.Errorf("--%s: %w", f.flag, err)
			}
			where, v = []string{"secrets", strings.Join(f.path[1:], ".")}, text
		}
		set := make([]step, len(where))
		for i, p := range where {
			set[i] = step{key: p}
		}
		if _, err := setAt(doc, set, v); err != nil {
			return nil, fmt.Errorf("--%s: %w", f.flag, err)
		}
	}
	for _, s := range oc.in.sets.values {
		where, value, ok := strings.Cut(s, "=")
		if !ok || where == "" {
			return nil, fmt.Errorf("--set takes path=value, such as spec.limits.cpu=200, not %q", s)
		}
		steps, err := parsePath(where)
		if err != nil {
			return nil, fmt.Errorf("--set %s: %w", where, err)
		}
		if _, err := setAt(doc, steps, looseJSON(value)); err != nil {
			return nil, fmt.Errorf("--set %s: %w", where, err)
		}
	}
	return doc, nil
}

// currentBody reads what an update starts from: the fields of the thing as
// it is now that the update takes, its revision among them, so a change
// someone else made in between makes the update fail instead of being
// lost.
func (oc *opCommand) currentBody(ctx context.Context, c *Client, path string) (map[string]any, error) {
	resp, err := c.Do(ctx, Request{Method: http.MethodGet, Path: path})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var current map[string]any
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxAnswer))
	dec.UseNumber()
	if err := dec.Decode(&current); err != nil {
		return nil, fmt.Errorf("unexpected answer from %s: %w", c.Server, err)
	}
	body := oc.doc.Resolve(oc.op.Body)
	out := map[string]any{}
	for k, v := range current {
		if body == nil || body.Properties[k] != nil {
			out[k] = v
		}
	}
	return out, nil
}

// readDocument reads JSON or YAML from a file, or from standard input
// for -.
func readDocument(name string, stdin io.Reader) (any, error) {
	var raw []byte
	var err error
	if name == "-" {
		raw, err = io.ReadAll(io.LimitReader(stdin, maxAnswer))
	} else {
		raw, err = os.ReadFile(name)
	}
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("reading %s: %w", name, err)
		}
		return v, nil
	}
	var v any
	if err := yaml.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	return v, nil
}

// step is one step of a path into a document: a field or a list index.
type step struct {
	key     string
	index   int
	isIndex bool
}

// parsePath reads a path such as spec.domains[0].name.
func parsePath(p string) ([]step, error) {
	var steps []step
	for _, part := range strings.Split(p, ".") {
		name := part
		var indexes []int
		for strings.HasSuffix(name, "]") {
			open := strings.LastIndex(name, "[")
			if open < 0 {
				return nil, fmt.Errorf("%q has a ] without a [", part)
			}
			n, err := strconv.Atoi(name[open+1 : len(name)-1])
			if err != nil || n < 0 || n > 10000 {
				return nil, fmt.Errorf("%q isn't a list index", name[open:])
			}
			indexes = append([]int{n}, indexes...)
			name = name[:open]
		}
		if name == "" {
			return nil, fmt.Errorf("%q names no field", p)
		}
		steps = append(steps, step{key: name})
		for _, n := range indexes {
			steps = append(steps, step{index: n, isIndex: true})
		}
	}
	return steps, nil
}

// setAt puts v at the path in a document made of maps and lists, adding
// what's missing on the way, and returns the container.
func setAt(container any, steps []step, v any) (any, error) {
	if len(steps) == 0 {
		return v, nil
	}
	s := steps[0]
	if s.isIndex {
		list, ok := container.([]any)
		if container != nil && !ok {
			return nil, fmt.Errorf("[%d] is inside something that isn't a list", s.index)
		}
		for len(list) <= s.index {
			list = append(list, nil)
		}
		child, err := setAt(list[s.index], steps[1:], v)
		if err != nil {
			return nil, err
		}
		list[s.index] = child
		return list, nil
	}
	m, ok := container.(map[string]any)
	if container != nil && !ok {
		return nil, fmt.Errorf("%s is inside something that isn't an object", s.key)
	}
	if m == nil {
		m = map[string]any{}
	}
	child, err := setAt(m[s.key], steps[1:], v)
	if err != nil {
		return nil, err
	}
	m[s.key] = child
	return m, nil
}

// describeRequest writes the request a command would send, with secrets
// hidden, for --dry-run on operations the panel can't preview.
func describeRequest(w io.Writer, c *Client, req Request) error {
	u := c.Server + req.Path
	if len(req.Query) > 0 {
		u += "?" + req.Query.Encode()
	}
	fmt.Fprintf(w, "This would send %s %s", req.Method, u)
	if req.Body == nil {
		fmt.Fprintln(w, ", and nothing was sent.")
		return nil
	}
	fmt.Fprintln(w, " with this body, and nothing was sent:")
	raw, err := json.MarshalIndent(hideSecrets(req.Body, ""), "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", raw)
	return err
}

// hideSecrets replaces the values of fields that hold secrets.
func hideSecrets(v any, key string) any {
	lower := strings.ToLower(key)
	secret := lower == "secrets" || strings.Contains(lower, "password") || strings.Contains(lower, "secret") || lower == "token"
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			if secret {
				out[k] = "(hidden)"
				continue
			}
			out[k] = hideSecrets(item, k)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = hideSecrets(item, key)
		}
		return out
	case string:
		if secret && t != "" {
			return "(hidden)"
		}
	}
	return v
}
