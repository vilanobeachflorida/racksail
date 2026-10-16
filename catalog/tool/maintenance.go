package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

// Maintenance is the catalog's maintenance.yaml: which items are featured,
// broken or unmaintained. The index carries these flags to every panel.
type Maintenance struct {
	Featured []string `yaml:"featured"`
	// Broken items fail their install test or no longer work.
	Broken []Mark `yaml:"broken"`
	// Unmaintained items have no one looking after them. They are left out
	// of the index once they have been unmaintained for too long.
	Unmaintained []Mark `yaml:"unmaintained"`
}

// Mark flags an item, with the reason and the day it started.
type Mark struct {
	ID     string `yaml:"id"`
	Reason string `yaml:"reason"`
	Since  string `yaml:"since"`
}

// SinceTime parses Since.
func (m Mark) SinceTime() (time.Time, error) {
	t, err := time.Parse(time.DateOnly, m.Since)
	if err != nil {
		return time.Time{}, fmt.Errorf("since %q must be a date such as 2026-10-15", m.Since)
	}
	return t, nil
}

func loadMaintenance(file string) (Maintenance, error) {
	var m Maintenance
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil && !errors.Is(err, io.EOF) {
		return m, fmt.Errorf("reading it: %w", err)
	}
	return m, nil
}

func (c *Catalog) checkMaintenance(probs *problems) {
	m := c.Maintenance
	const where = "maintenance.yaml"
	featured := map[string]bool{}
	for _, id := range m.Featured {
		if featured[id] {
			probs.add(where, "%s is featured twice", id)
		}
		featured[id] = true
		if _, ok := c.Item(id); !ok {
			probs.add(where, "features %s, which isn't in the catalog", id)
		}
	}
	for _, list := range []struct {
		name  string
		marks []Mark
	}{{"broken", m.Broken}, {"unmaintained", m.Unmaintained}} {
		seen := map[string]bool{}
		for _, mark := range list.marks {
			if seen[mark.ID] {
				probs.add(where, "%s is listed as %s twice", mark.ID, list.name)
			}
			seen[mark.ID] = true
			if _, ok := c.Item(mark.ID); !ok {
				probs.add(where, "lists %s as %s, but it isn't in the catalog", mark.ID, list.name)
			}
			if featured[mark.ID] {
				probs.add(where, "%s is featured and %s; an item can't be both", mark.ID, list.name)
			}
			if mark.Reason == "" {
				probs.add(where, "%s is %s without a reason", mark.ID, list.name)
			}
			if _, err := mark.SinceTime(); err != nil {
				probs.add(where, "%s: %v", mark.ID, err)
			}
		}
	}
}

func (m Maintenance) mark(list []Mark, id string) (Mark, bool) {
	for _, mk := range list {
		if mk.ID == id {
			return mk, true
		}
	}
	return Mark{}, false
}
