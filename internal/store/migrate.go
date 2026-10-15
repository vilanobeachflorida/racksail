package store

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Migration is one change to the panel database schema. The core and each
// module keep their migrations as embedded files named NNNN_description.sql;
// the SQLite store applies them in order, once, per owner.
type Migration struct {
	// Owner is "core" or a module id.
	Owner   string
	Version int
	Name    string
	SQL     string
}

var migrationName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

// LoadMigrations reads the migrations in the root of fsys. Versions must
// start at 1 and have no gaps, so a missing file is caught before anything
// runs.
func LoadMigrations(owner string, fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("reading migrations of %s: %w", owner, err)
	}
	var out []Migration
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".sql" {
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration %s of %s: name must look like 0001_create_things.sql", e.Name(), owner)
		}
		version, _ := strconv.Atoi(m[1])
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("reading migration %s of %s: %w", e.Name(), owner, err)
		}
		if strings.TrimSpace(string(body)) == "" {
			return nil, fmt.Errorf("migration %s of %s is empty", e.Name(), owner)
		}
		out = append(out, Migration{Owner: owner, Version: version, Name: m[2], SQL: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, m := range out {
		if m.Version != i+1 {
			return nil, fmt.Errorf("migrations of %s: expected version %04d, found %04d_%s", owner, i+1, m.Version, m.Name)
		}
	}
	return out, nil
}
