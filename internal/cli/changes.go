package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// changeSet is what the preview needs of a change set.
type changeSet struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Changes []change `json:"changes"`
	Effects []struct {
		Type          string `json:"type"`
		Action        string `json:"action"`
		Target        string `json:"target"`
		Detail        string `json:"detail"`
		DownloadBytes int64  `json:"downloadBytes"`
		Destructive   bool   `json:"destructive"`
		Contributor   string `json:"contributor"`
	} `json:"effects"`
	Contributions []struct {
		Source  source `json:"source"`
		Summary string `json:"summary"`
		Changes []int  `json:"changes"`
		Effects []int  `json:"effects"`
	} `json:"contributions"`
	Conflicts []struct {
		Resource string   `json:"resource"`
		Path     string   `json:"path"`
		Sources  []source `json:"sources"`
		Message  string   `json:"message"`
	} `json:"conflicts"`
	Risk     string `json:"risk"`
	Estimate struct {
		DownloadBytes int64 `json:"downloadBytes"`
		DiskBytes     int64 `json:"diskBytes"`
		MemoryBytes   int64 `json:"memoryBytes"`
	} `json:"estimate"`
	Snapshot bool   `json:"snapshot"`
	DryRun   bool   `json:"dryRun"`
	State    string `json:"state"`
	Job      string `json:"job"`
	UndoOf   string `json:"undoOf"`
	Error    string `json:"error"`
}

// change is one change of a change set.
type change struct {
	Op     string `json:"op"`
	Action string `json:"action"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Diff   []struct {
		Path      string          `json:"path"`
		Op        string          `json:"op"`
		Before    json.RawMessage `json:"before"`
		After     json.RawMessage `json:"after"`
		Sensitive bool            `json:"sensitive"`
	} `json:"diff"`
	Input  json.RawMessage `json:"input"`
	Source source          `json:"source"`
	Output json.RawMessage `json:"output"`
}

type source struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Hook string `json:"hook"`
}

func (s source) String() string {
	name := s.Type
	if s.Name != "" {
		name += " " + s.Name
	}
	if s.Hook != "" {
		name += " (through " + s.Hook + ")"
	}
	return name
}

// isChangeSet recognises a change set among answers.
func isChangeSet(v any) bool {
	o, ok := v.(*object)
	if !ok {
		return false
	}
	_, changes := o.get("changes").([]any)
	_, state := o.get("state").(string)
	_, risk := o.get("risk").(string)
	return changes && state && risk
}

// printChangeSet writes a change set for people: each change with what it
// changes, what happens on the server, what hooks and add-ons added, and
// what it costs.
func printChangeSet(w io.Writer, raw []byte) error {
	var cs changeSet
	if err := json.Unmarshal(raw, &cs); err != nil {
		return err
	}
	title := cs.Title
	if title == "" {
		title = "Change set"
	}
	fmt.Fprintf(w, "%s (%s risk)\n", title, cs.Risk)
	for _, c := range cs.Changes {
		op := c.Op
		if op == "action" {
			op = c.Action
		}
		target := c.ID
		if target == "" {
			target = "(new)"
		}
		fmt.Fprintf(w, "  %s %s %s", op, c.Kind, target)
		if c.Source.Type != "" && c.Source.Type != "request" {
			fmt.Fprintf(w, "  added by %s", c.Source)
		}
		fmt.Fprintln(w)
		for _, d := range c.Diff {
			fmt.Fprintf(w, "      %s %s\n", diffMark(d.Op), diffLine(d.Path, d.Op, d.Before, d.After, d.Sensitive))
		}
		if c.Op == "action" && len(c.Input) > 0 && string(c.Input) != "null" && string(c.Input) != "{}" {
			fmt.Fprintf(w, "      with %s\n", compact(c.Input))
		}
	}
	if len(cs.Effects) > 0 {
		fmt.Fprintln(w, "On the server:")
		for _, e := range cs.Effects {
			line := strings.TrimSpace(e.Action + " " + e.Type + " " + e.Target)
			if e.Detail != "" {
				line += " " + e.Detail
			}
			if e.DownloadBytes > 0 {
				line += ", downloading " + humanBytes(e.DownloadBytes)
			}
			if e.Destructive {
				line += " (removes data)"
			}
			if e.Contributor != "" {
				line += "  added by " + e.Contributor
			}
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	if len(cs.Contributions) > 0 {
		fmt.Fprintln(w, "Added by hooks and add-ons:")
		for _, c := range cs.Contributions {
			fmt.Fprintf(w, "  %s: %s\n", c.Source, c.Summary)
		}
	}
	if len(cs.Conflicts) > 0 {
		fmt.Fprintln(w, "Conflicts, which keep it from being applied:")
		for _, c := range cs.Conflicts {
			fmt.Fprintf(w, "  %s %s: %s\n", c.Resource, c.Path, c.Message)
		}
	}
	var costs []string
	if n := cs.Estimate.DownloadBytes; n > 0 {
		costs = append(costs, humanBytes(n)+" to download")
	}
	if n := cs.Estimate.DiskBytes; n > 0 {
		costs = append(costs, humanBytes(n)+" of disk")
	}
	if n := cs.Estimate.MemoryBytes; n > 0 {
		costs = append(costs, humanBytes(n)+" of memory")
	}
	if len(costs) > 0 {
		fmt.Fprintf(w, "It takes about %s.\n", strings.Join(costs, ", "))
	}
	if cs.Snapshot {
		fmt.Fprintln(w, "It removes data, so snapshots are taken before it's applied.")
	}
	switch {
	case cs.DryRun:
		fmt.Fprintln(w, "This is a preview: nothing was changed.")
	case cs.Error != "":
		fmt.Fprintf(w, "It failed: %s\n", cs.Error)
	case cs.State == "applied":
		fmt.Fprintf(w, "Applied as %s.\n", cs.ID)
	case cs.State == "undone":
		fmt.Fprintf(w, "Undone (%s).\n", cs.ID)
	}
	return nil
}

func diffMark(op string) string {
	switch op {
	case "add":
		return "+"
	case "remove":
		return "-"
	}
	return "~"
}

func diffLine(path, op string, before, after json.RawMessage, sensitive bool) string {
	if sensitive {
		return path + ": (secret)"
	}
	switch op {
	case "add":
		return path + ": " + compact(after)
	case "remove":
		return path + ": " + compact(before)
	}
	return path + ": " + compact(before) + " → " + compact(after)
}

// compact writes JSON on one line, cut short when long.
func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	text := b.String()
	if len([]rune(text)) > 100 {
		text = string([]rune(text)[:99]) + "…"
	}
	return text
}

// printOutputs writes what the actions of an applied change set returned.
func printOutputs(w io.Writer, raw []byte, format string) error {
	var cs changeSet
	if err := json.Unmarshal(raw, &cs); err != nil {
		return err
	}
	for _, c := range cs.Changes {
		if c.Op != "action" || len(c.Output) == 0 || string(c.Output) == "null" {
			continue
		}
		if format == "table" {
			fmt.Fprintf(w, "%s returned:\n", c.Action)
			v, err := decodeOrdered(c.Output)
			if err != nil {
				return err
			}
			if err := printTable(w, v, nil); err != nil {
				return err
			}
			continue
		}
		if err := writeJSON(w, c.Output); err != nil {
			return err
		}
	}
	return nil
}
