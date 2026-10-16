package secrets

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// FileStore keeps secrets encrypted in a directory, one file per secret
// under a directory per owner. Only the panel's user can read it, and the
// files are useless without the key, which is kept elsewhere.
type FileStore struct {
	dir  string
	box  *Box
	info KeyInfo
	// mu orders writes; reads see whole files thanks to atomic renames.
	mu sync.Mutex
}

// keyCheckFile holds a known value encrypted with the key, so a wrong key
// is recognized at start instead of when a secret is first needed.
const keyCheckFile = "key-check"

var keyCheckRef = Ref("racksail/key-check")

const keyCheckValue = "racksail secrets"

// tempPrefix marks files being written.
const tempPrefix = ".tmp-"

func (f *FileStore) file(ref Ref) (string, error) {
	owner, name, err := ref.Parse()
	if err != nil {
		return "", err
	}
	return filepath.Join(f.dir, string(owner), name), nil
}

// Info says where the key came from and how it is kept.
func (f *FileStore) Info() KeyInfo { return f.info }

// Put implements Store.
func (f *FileStore) Put(ctx context.Context, ref Ref, value []byte) error {
	p, err := f.file(ref)
	if err != nil {
		return err
	}
	sealed, err := f.box.Encrypt(ref, value)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.Mkdir(filepath.Dir(p), 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return writeAtomic(p, sealed)
}

// Get implements Store.
func (f *FileStore) Get(ctx context.Context, ref Ref) ([]byte, error) {
	p, err := f.file(ref)
	if err != nil {
		return nil, err
	}
	sealed, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound.New("ref", string(ref))
	}
	if err != nil {
		return nil, err
	}
	return f.box.Decrypt(ref, sealed)
}

// Delete implements Store.
func (f *FileStore) Delete(ctx context.Context, ref Ref) error {
	p, err := f.file(ref)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.Remove(p); errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound.New("ref", string(ref))
	} else if err != nil {
		return err
	}
	// The owner's directory goes with its last secret; it stays when
	// something else is in it.
	os.Remove(filepath.Dir(p))
	return nil
}

// List implements Store.
func (f *FileStore) List(ctx context.Context, owner store.ID) ([]Ref, error) {
	if !owner.Valid() {
		return nil, ErrBadRef.New("ref", string(owner)+"/")
	}
	entries, err := os.ReadDir(filepath.Join(f.dir, string(owner)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Ref
	for _, e := range entries {
		ref := NewRef(owner, e.Name())
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		if _, _, err := ref.Parse(); err == nil {
			out = append(out, ref)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// verify reports whether box opens what the directory holds: the key check
// when there is one, or else the first secret found.
func (f *FileStore) verify(box *Box) error {
	if sealed, err := os.ReadFile(filepath.Join(f.dir, keyCheckFile)); err == nil {
		_, err := box.Decrypt(keyCheckRef, sealed)
		return err
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	ref, sealed, err := f.anySecret()
	if err != nil || ref == "" {
		return err
	}
	_, err = box.Decrypt(ref, sealed)
	return err
}

// hasData reports whether the directory holds anything encrypted.
func (f *FileStore) hasData() (bool, error) {
	if _, err := os.Stat(filepath.Join(f.dir, keyCheckFile)); err == nil {
		return true, nil
	}
	ref, _, err := f.anySecret()
	return ref != "", err
}

func (f *FileStore) anySecret() (Ref, []byte, error) {
	owners, err := os.ReadDir(f.dir)
	if err != nil {
		return "", nil, err
	}
	for _, o := range owners {
		if !o.IsDir() || !store.ID(o.Name()).Valid() {
			continue
		}
		refs, err := f.List(context.Background(), store.ID(o.Name()))
		if err != nil {
			return "", nil, err
		}
		for _, ref := range refs {
			p, _ := f.file(ref)
			sealed, err := os.ReadFile(p)
			if err != nil {
				return "", nil, err
			}
			return ref, sealed, nil
		}
	}
	return "", nil, nil
}

// writeKeyCheck records that box's key encrypts this directory.
func (f *FileStore) writeKeyCheck() error {
	sealed, err := f.box.Encrypt(keyCheckRef, []byte(keyCheckValue))
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(f.dir, keyCheckFile), sealed)
}

// writeAtomic replaces a file with data, readable by its owner only, so a
// crash leaves the old contents or the new ones.
func writeAtomic(p string, data []byte) error {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(p), tempPrefix+hex.EncodeToString(suffix))
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("secrets: writing %s: %w", p, err)
	}
	if d, err := os.Open(filepath.Dir(p)); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
