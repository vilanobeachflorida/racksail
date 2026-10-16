package secrets

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

// memorySealer seals into memory, standing in for systemd-creds.
type memorySealer struct {
	sealed  []byte
	sealErr error
	openErr error
	seals   int
}

func (m *memorySealer) Seal(ctx context.Context, key []byte) (string, error) {
	if m.sealErr != nil {
		return "", m.sealErr
	}
	m.seals++
	m.sealed = bytes.Clone(key)
	return "tpm2+host", nil
}

func (m *memorySealer) Unseal(ctx context.Context) ([]byte, error) {
	if m.openErr != nil {
		return nil, m.openErr
	}
	return bytes.Clone(m.sealed), nil
}

func TestFileStoreKeepsSecretsAcrossStarts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sealer := &memorySealer{}
	opts := Options{Dir: filepath.Join(dir, "secrets"), Sealer: sealer, KeyFile: filepath.Join(dir, "secrets.key")}
	s, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if info := s.Info(); info.Source != SourceSealed || !info.Created || info.Protection != "tpm2+host" || sealer.seals != 1 {
		t.Fatalf("info = %+v, seals %d", info, sealer.seals)
	}
	if _, err := os.Stat(opts.KeyFile); !errors.Is(err, os.ErrNotExist) {
		t.Error("a sealed key needs no key file")
	}
	site := store.NewID("site")
	ref := NewRef(site, "db-password")
	if err := s.Put(ctx, ref, []byte("hunter22")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, NewRef(site, "api-token"), []byte("tok")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(opts.Dir, string(site), "db-password"))
	if err != nil || bytes.Contains(raw, []byte("hunter22")) {
		t.Fatalf("the value must be encrypted on disk: %v", err)
	}
	info, _ := os.Stat(filepath.Join(opts.Dir, string(site), "db-password"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode())
	}

	// The next start opens the key systemd imported.
	creds := filepath.Join(dir, "credentials")
	os.MkdirAll(creds, 0o700)
	os.WriteFile(filepath.Join(creds, CredentialName), sealer.sealed, 0o400)
	again, err := Open(ctx, Options{Dir: opts.Dir, CredentialsDir: creds, Sealer: sealer, KeyFile: opts.KeyFile})
	if err != nil {
		t.Fatal(err)
	}
	if again.Info().Source != SourceCredential || again.Info().Created {
		t.Errorf("info = %+v", again.Info())
	}
	got, err := again.Get(ctx, ref)
	if err != nil || string(got) != "hunter22" {
		t.Errorf("get = %q, %v", got, err)
	}
	refs, _ := again.List(ctx, site)
	if len(refs) != 2 || refs[0] != NewRef(site, "api-token") {
		t.Errorf("list = %v", refs)
	}

	// Without the import, the sealer opens it.
	third, err := Open(ctx, Options{Dir: opts.Dir, Sealer: sealer, KeyFile: opts.KeyFile})
	if err != nil || third.Info().Source != SourceSealed || third.Info().Created {
		t.Fatalf("third = %+v, %v", third.Info(), err)
	}
	if err := third.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := third.Get(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
	if err := third.Delete(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
	if err := third.Delete(ctx, NewRef(site, "api-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(opts.Dir, string(site))); !errors.Is(err, os.ErrNotExist) {
		t.Error("the owner's directory goes with its last secret")
	}
	if err := third.Put(ctx, "../../etc/passwd", []byte("x")); !errors.Is(err, ErrBadRef) {
		t.Errorf("a ref that climbs out: %v", err)
	}
	if refs, err := third.List(ctx, store.NewID("site")); err != nil || len(refs) != 0 {
		t.Errorf("an owner without secrets = %v, %v", refs, err)
	}
}

func TestKeyFileFallback(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	opts := Options{Dir: filepath.Join(dir, "secrets"), Sealer: &memorySealer{sealErr: errors.New("systemd-creds is missing")}, KeyFile: filepath.Join(dir, "secrets.key")}
	s, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if s.Info().Source != SourceKeyFile {
		t.Fatalf("info = %+v", s.Info())
	}
	raw, err := os.ReadFile(opts.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if key, err := hex.DecodeString(strings.TrimSpace(string(raw))); err != nil || len(key) != KeySize {
		t.Errorf("key file = %q", raw)
	}
	if info, _ := os.Stat(opts.KeyFile); info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v", info.Mode())
	}
	ref := NewRef(store.NewID("site"), "pw")
	if err := s.Put(ctx, ref, []byte("v")); err != nil {
		t.Fatal(err)
	}
	// The sealer has nothing; the key file still opens the secrets.
	again, err := Open(ctx, Options{Dir: opts.Dir, Sealer: &memorySealer{}, KeyFile: opts.KeyFile})
	if err != nil || again.Info().Source != SourceKeyFile {
		t.Fatalf("reopen = %+v, %v", again.Info(), err)
	}
	if v, err := again.Get(ctx, ref); err != nil || string(v) != "v" {
		t.Errorf("get = %q, %v", v, err)
	}
}

func TestAWrongOrMissingKeyIsNeverReplaced(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sealer := &memorySealer{}
	opts := Options{Dir: filepath.Join(dir, "secrets"), Sealer: sealer, KeyFile: filepath.Join(dir, "secrets.key")}
	s, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	ref := NewRef(store.NewID("site"), "pw")
	if err := s.Put(ctx, ref, []byte("v")); err != nil {
		t.Fatal(err)
	}

	// The sealed key is gone, say after the TPM was reset.
	lost := &memorySealer{}
	if _, err := Open(ctx, Options{Dir: opts.Dir, Sealer: lost, KeyFile: opts.KeyFile}); err == nil || !strings.Contains(err.Error(), CredentialName) {
		t.Errorf("a missing key must stop the start: %v", err)
	}
	if lost.seals != 0 {
		t.Error("no new key may be made while there are secrets the old one opens")
	}

	// A key that doesn't open the secrets is skipped for one that does.
	creds := filepath.Join(dir, "credentials")
	os.MkdirAll(creds, 0o700)
	os.WriteFile(filepath.Join(creds, CredentialName), bytes.Repeat([]byte{9}, KeySize), 0o400)
	right, err := Open(ctx, Options{Dir: opts.Dir, CredentialsDir: creds, Sealer: sealer, KeyFile: opts.KeyFile})
	if err != nil || right.Info().Source != SourceSealed {
		t.Fatalf("open = %+v, %v", right, err)
	}

	// A sealer that can't be reached is not the same as no key.
	if _, err := Open(ctx, Options{Dir: filepath.Join(dir, "empty"), Sealer: &memorySealer{openErr: errors.New("agent unreachable")}}); err == nil {
		t.Error("an unreachable sealer must not lead to a new key")
	}
}

func TestMemoryOnlyKey(t *testing.T) {
	s, err := Open(context.Background(), Options{Dir: t.TempDir()})
	if err != nil || s.Info().Source != SourceMemory {
		t.Errorf("open = %+v, %v", s, err)
	}
	if _, err := Open(context.Background(), Options{}); err == nil {
		t.Error("a store needs a directory")
	}
}

// In development the simulated server seals the key, as systemd-creds does
// on a real one, and keeps it across restarts of racksail dev.
func TestSealedThroughTheSimulatedAgent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	machine, err := sim.Open(filepath.Join(dir, "machine"), sim.Options{Hostname: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	sealer := agent.KeySealer{Client: agent.NewLocal(machine.Backend(), nil), Name: CredentialName}
	opts := Options{Dir: filepath.Join(dir, "secrets"), Sealer: sealer, KeyFile: filepath.Join(dir, "secrets.key")}
	s, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if s.Info().Source != SourceSealed || s.Info().Protection != agent.ProtectionTPM2 {
		t.Fatalf("info = %+v", s.Info())
	}
	ref := NewRef(store.NewID("site"), "pw")
	if err := s.Put(ctx, ref, []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := machine.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sim.Open(filepath.Join(dir, "machine"), sim.Options{Hostname: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	opts.Sealer = agent.KeySealer{Client: agent.NewLocal(reopened.Backend(), nil), Name: CredentialName}
	again, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := again.Get(ctx, ref); err != nil || string(v) != "v" {
		t.Errorf("get after a restart = %q, %v", v, err)
	}
}
