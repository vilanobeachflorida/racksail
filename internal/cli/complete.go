package cli

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// completionTimeout bounds how long completing an argument waits for the
// panel, so a slow one never hangs the shell.
const completionTimeout = 4 * time.Second

// completeArgs completes an operation's arguments with what the panel
// lists: the IDs of resources, users or jobs, with their names.
func (oc *opCommand) completeArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	params := oc.op.PathParams()
	if len(args) >= len(params) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	list := oc.listFor(len(args))
	if list == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	c, err := oc.opts.Connect()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
	defer cancel()
	path := list.Path
	for i, p := range list.PathParams() {
		path = strings.Replace(path, "{"+p.Name+"}", url.PathEscape(args[i]), 1)
	}
	query := url.Values{}
	if list.Param("query", "limit") != nil {
		query.Set("limit", "200")
	}
	resp, err := c.Do(ctx, Request{Method: http.MethodGet, Path: path, Query: query})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return candidates(raw, params[len(args)].Name, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// listFor finds the operation that lists what the nth argument names: the
// GET of the path up to that argument, such as /api/v1/core/users for the
// user in /api/v1/core/users/{id}/tokens/{token}.
func (oc *opCommand) listFor(n int) *Operation {
	params := oc.op.PathParams()
	marker := "/{" + params[n].Name + "}"
	i := strings.Index(oc.op.Path, marker)
	if i < 0 {
		return nil
	}
	return oc.doc.At(http.MethodGet, oc.op.Path[:i])
}

// candidates are the IDs, or names, of the items a list answered with,
// each with what it's called.
func candidates(raw []byte, param, prefix string) []string {
	v, err := decodeOrdered(raw)
	if err != nil {
		return nil
	}
	var items []any
	switch t := v.(type) {
	case []any:
		items = t
	case *object:
		for _, k := range t.keys {
			if list, ok := t.values[k].([]any); ok {
				items = list
				break
			}
		}
	}
	var out []string
	for _, item := range items {
		o, ok := item.(*object)
		if !ok {
			continue
		}
		key := "id"
		if o.get("id") == nil {
			key = param
		}
		id, _ := o.get(key).(string)
		if id == "" || !strings.HasPrefix(id, prefix) {
			continue
		}
		label := ""
		for _, k := range []string{"name", "username", "title", "slug"} {
			if text, ok := o.get(k).(string); ok && text != "" && text != id {
				label = text
				break
			}
		}
		if spec, ok := o.get("spec").(*object); ok && label == "" {
			for _, k := range []string{"name", "username", "title", "slug", "domain", "url"} {
				if text, ok := spec.get(k).(string); ok && text != "" {
					label = text
					break
				}
			}
		}
		if label != "" {
			id += "\t" + label
		}
		out = append(out, id)
	}
	return out
}
