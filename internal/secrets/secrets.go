package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Placeholder replaces secret values wherever they would be shown.
const Placeholder = "••••••••"

// KeySize is the size of a Box key.
const KeySize = 32

// Errors.
var (
	ErrNotFound = i18n.Define("secrets.not_found", http.StatusNotFound, "The secret {ref} doesn't exist.")
	ErrBadRef   = i18n.Define("secrets.bad_ref", http.StatusUnprocessableEntity, "{ref} isn't a secret reference; use the owner's id, a slash and the secret's name.")
)

// Ref names a secret: the ID of the resource that owns it and a name.
type Ref string

var refNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// NewRef builds a reference.
func NewRef(owner store.ID, name string) Ref { return Ref(string(owner) + "/" + name) }

// Parse splits a reference into owner and name.
func (r Ref) Parse() (store.ID, string, error) {
	owner, name, ok := strings.Cut(string(r), "/")
	if !ok || !store.ID(owner).Valid() || !refNamePattern.MatchString(name) {
		return "", "", ErrBadRef.New("ref", string(r))
	}
	return store.ID(owner), name, nil
}

// Reader reads secret values. Reconcilers get one to resolve the
// references in specs at the moment they need a value.
type Reader interface {
	Get(ctx context.Context, ref Ref) ([]byte, error)
}

// Store keeps secrets.
type Store interface {
	Reader
	Put(ctx context.Context, ref Ref, value []byte) error
	Delete(ctx context.Context, ref Ref) error
	// List returns the refs owned by a resource.
	List(ctx context.Context, owner store.ID) ([]Ref, error)
}

// Box encrypts secret values.
type Box struct {
	aead cipher.AEAD
}

// NewKey returns a random key for a Box.
func NewKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

// NewBox returns a box using a 32-byte key.
func NewBox(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secrets: key must be %d bytes", KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Encrypt encrypts a value for a ref. The output is nonce then ciphertext.
func (b *Box) Encrypt(ref Ref, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, []byte(ref)), nil
}

// Decrypt reverses Encrypt. It fails if the value was encrypted for another
// ref or was tampered with.
func (b *Box) Decrypt(ref Ref, sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("secrets: value too short")
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(ref))
	if err != nil {
		return nil, fmt.Errorf("secrets: can't decrypt %s: %w", ref, err)
	}
	return plain, nil
}

// Memory is a Store that keeps encrypted values in memory.
type Memory struct {
	box  *Box
	mu   sync.RWMutex
	data map[Ref][]byte
}

// NewMemory returns an empty store encrypting with box.
func NewMemory(box *Box) *Memory {
	return &Memory{box: box, data: map[Ref][]byte{}}
}

// Put implements Store.
func (m *Memory) Put(ctx context.Context, ref Ref, value []byte) error {
	if _, _, err := ref.Parse(); err != nil {
		return err
	}
	sealed, err := m.box.Encrypt(ref, value)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[ref] = sealed
	return nil
}

// Get implements Store.
func (m *Memory) Get(ctx context.Context, ref Ref) ([]byte, error) {
	m.mu.RLock()
	sealed, ok := m.data[ref]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound.New("ref", string(ref))
	}
	return m.box.Decrypt(ref, sealed)
}

// Delete implements Store.
func (m *Memory) Delete(ctx context.Context, ref Ref) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[ref]; !ok {
		return ErrNotFound.New("ref", string(ref))
	}
	delete(m.data, ref)
	return nil
}

// List implements Store.
func (m *Memory) List(ctx context.Context, owner store.ID) ([]Ref, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Ref
	for ref := range m.data {
		if o, _, err := ref.Parse(); err == nil && o == owner {
			out = append(out, ref)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// Redact replaces every occurrence of the given secret values in s with
// Placeholder, for logs and assistant context.
func Redact(s string, values ...string) string {
	values = slices.DeleteFunc(slices.Clone(values), func(v string) bool { return len(v) < 4 })
	// Longest first, so a secret containing another is replaced whole.
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		s = strings.ReplaceAll(s, v, Placeholder)
	}
	return s
}
