package addons

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Tokens are how add-ons prove who they are when they call the panel's
// API through the gateway. Each running add-on has one, which the panel
// writes where systemd hands it to the add-on as a credential, and keeps
// only as a hash.
type Tokens interface {
	// Put replaces an add-on's token with the one hashed.
	Put(ctx context.Context, instance string, hash []byte) error
	// Lookup returns the add-on a token hash belongs to.
	Lookup(ctx context.Context, hash []byte) (string, error)
	// Delete forgets an add-on's token.
	Delete(ctx context.Context, instance string) error
}

// ErrNoToken says a token isn't an add-on's.
var ErrNoToken = errors.New("addons: the token isn't one an add-on holds")

// tokenPrefix starts every add-on token, so one that leaks is easy to
// recognize.
const tokenPrefix = "rsaddon_"

// NewToken returns a new token and its hash.
func NewToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token := tokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken returns what is kept of a token.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// IsToken reports whether a string looks like an add-on token.
func IsToken(s string) bool { return strings.HasPrefix(s, tokenPrefix) }

// MemoryTokens keeps token hashes in memory, for tests and tools.
type MemoryTokens struct {
	mu sync.Mutex
	m  map[string][]byte
}

// NewMemoryTokens returns an empty in-memory token store.
func NewMemoryTokens() *MemoryTokens { return &MemoryTokens{m: map[string][]byte{}} }

// Put implements Tokens.
func (t *MemoryTokens) Put(ctx context.Context, instance string, hash []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m[instance] = hash
	return nil
}

// Lookup implements Tokens.
func (t *MemoryTokens) Lookup(ctx context.Context, hash []byte) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for inst, h := range t.m {
		if string(h) == string(hash) {
			return inst, nil
		}
	}
	return "", ErrNoToken
}

// Delete implements Tokens.
func (t *MemoryTokens) Delete(ctx context.Context, instance string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, instance)
	return nil
}

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the migrations of the add-ons' tables.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// MigrationOwner is the owner the add-ons' migrations run under.
const MigrationOwner = "core.addons"

// SQLTokens keeps token hashes in the panel database, so add-ons that keep
// running while the panel restarts can still reach it.
type SQLTokens struct{ db *store.DB }

// NewSQLTokens brings the add-ons' tables up to date and returns a token
// store kept in them.
func NewSQLTokens(ctx context.Context, db *store.DB) (*SQLTokens, error) {
	if err := db.Migrate(ctx, MigrationOwner, Migrations()); err != nil {
		return nil, err
	}
	return &SQLTokens{db: db}, nil
}

// Put implements Tokens.
func (t *SQLTokens) Put(ctx context.Context, instance string, hash []byte) error {
	return t.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO addon_tokens (instance, hash, created_at) VALUES (?, ?, ?)
			ON CONFLICT (instance) DO UPDATE SET hash = excluded.hash, created_at = excluded.created_at`,
			instance, hash, store.FormatTime(time.Now()))
		return err
	})
}

// Lookup implements Tokens.
func (t *SQLTokens) Lookup(ctx context.Context, hash []byte) (string, error) {
	var inst string
	err := t.db.Reader().QueryRowContext(ctx, `SELECT instance FROM addon_tokens WHERE hash = ?`, hash).Scan(&inst)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoToken
	}
	return inst, err
}

// Delete implements Tokens.
func (t *SQLTokens) Delete(ctx context.Context, instance string) error {
	_, err := t.db.Exec(ctx, `DELETE FROM addon_tokens WHERE instance = ?`, instance)
	return err
}

// writeToken puts a token where systemd reads it for the add-on: a file
// only the panel's user can read, replaced in one step.
func writeToken(dir, instance, token string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+instance+".token-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(token); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, instance+".token"))
}

// readTokenFile reads an add-on's token file.
func readTokenFile(dir, instance string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, instance+".token"))
	if err != nil {
		return "", err
	}
	if !IsToken(string(data)) {
		return "", ErrNoToken
	}
	return string(data), nil
}

// removeToken deletes an add-on's token file.
func removeToken(dir, instance string) error {
	err := os.Remove(filepath.Join(dir, instance+".token"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
