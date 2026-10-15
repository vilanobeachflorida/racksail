package secrets

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// The key that encrypts secrets is looked for, in order:
//
//  1. in the credential systemd imported for the panel at start
//     (ImportCredential= in racksail-panel.service),
//  2. through the Sealer, which opens the sealed credential when systemd
//     didn't import it, as when the panel runs outside systemd,
//  3. in the key file, for machines where the key couldn't be sealed.
//
// The first start makes a key and seals it, or writes the key file when it
// can't be sealed. A key that doesn't open what the directory already
// holds is never used, and no new key is made while there is data a
// missing key would have opened.

// CredentialName is the systemd credential holding the key.
const CredentialName = system.SecretsCredential

// Sealer seals the key where only this machine can open it: systemd-creds
// through the agent on a server, the simulated machine in development.
type Sealer interface {
	// Seal keeps key sealed and says how: tpm2+host or host.
	Seal(ctx context.Context, key []byte) (protection string, err error)
	// Unseal returns the sealed key, or nil when none was sealed.
	Unseal(ctx context.Context) ([]byte, error)
}

// KeySource says where the key came from.
type KeySource string

// Key sources.
const (
	// SourceCredential is the credential systemd imported.
	SourceCredential KeySource = "credential"
	// SourceSealed is the sealed credential opened through the Sealer.
	SourceSealed KeySource = "sealed"
	// SourceKeyFile is the key file.
	SourceKeyFile KeySource = "file"
	// SourceMemory is a key that lives as long as the process, for tests.
	SourceMemory KeySource = "memory"
)

// KeyInfo describes the key in use.
type KeyInfo struct {
	Source KeySource `json:"source"`
	// Protection is how a key sealed now is protected: tpm2+host or host.
	Protection string `json:"protection,omitempty"`
	// Created is set when the key was made at this start.
	Created bool `json:"created,omitempty"`
}

// Options configures Open.
type Options struct {
	// Dir keeps the encrypted values.
	Dir string
	// CredentialsDir is where systemd put the credentials the service
	// imports: $CREDENTIALS_DIRECTORY.
	CredentialsDir string
	// Sealer seals a new key, and opens it when systemd didn't import it.
	Sealer Sealer
	// KeyFile holds the key when it can't be sealed. Without a sealer and a
	// key file, the key lives only as long as the process.
	KeyFile string
	Log     *slog.Logger
}

// Open opens the secrets in opts.Dir, finding their key or making one.
func Open(ctx context.Context, opts Options) (*FileStore, error) {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opts.Dir == "" {
		return nil, errors.New("secrets: no directory to keep secrets in")
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, err
	}
	f := &FileStore{dir: opts.Dir}

	type candidate struct {
		source KeySource
		load   func() ([]byte, error)
	}
	candidates := []candidate{
		{SourceCredential, func() ([]byte, error) {
			if opts.CredentialsDir == "" {
				return nil, nil
			}
			return readIfExists(filepath.Join(opts.CredentialsDir, CredentialName))
		}},
		{SourceSealed, func() ([]byte, error) {
			if opts.Sealer == nil {
				return nil, nil
			}
			return opts.Sealer.Unseal(ctx)
		}},
		{SourceKeyFile, func() ([]byte, error) {
			if opts.KeyFile == "" {
				return nil, nil
			}
			raw, err := readIfExists(opts.KeyFile)
			if raw == nil || err != nil {
				return nil, err
			}
			key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
			if err != nil {
				return nil, fmt.Errorf("the key file %s isn't a hex key", opts.KeyFile)
			}
			return key, nil
		}},
	}
	var problems []error
	for _, c := range candidates {
		key, err := c.load()
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", c.source, err))
			continue
		}
		if key == nil {
			continue
		}
		box, err := NewBox(key)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", c.source, err))
			continue
		}
		if err := f.verify(box); err != nil {
			log.Warn("a secrets key doesn't open the secrets kept; trying the next place", "source", c.source, "error", err)
			problems = append(problems, fmt.Errorf("%s: the key doesn't open the secrets kept", c.source))
			continue
		}
		f.box, f.info = box, KeyInfo{Source: c.source}
		return f, nil
	}

	data, err := f.hasData()
	if err != nil {
		return nil, err
	}
	if data || len(problems) > 0 {
		// Making a new key now would leave what the old one encrypted
		// unreadable for good, so stop and say what was tried.
		return nil, fmt.Errorf("secrets: the key for %s can't be found or opened (%w); restore the key file or the sealed credential %s",
			opts.Dir, errors.Join(problems...), CredentialName)
	}

	key, err := NewKey()
	if err != nil {
		return nil, err
	}
	if f.box, err = NewBox(key); err != nil {
		return nil, err
	}
	f.info = KeyInfo{Source: SourceMemory, Created: true}
	sealed := false
	if opts.Sealer != nil {
		protection, err := opts.Sealer.Seal(ctx, key)
		switch {
		case err == nil:
			f.info.Source, f.info.Protection, sealed = SourceSealed, protection, true
			log.Info("made the secrets key and sealed it", "protection", protection)
		case opts.KeyFile == "":
			return nil, fmt.Errorf("secrets: sealing the new key: %w", err)
		default:
			log.Warn("the secrets key can't be sealed; keeping it in a file only the panel can read", "file", opts.KeyFile, "error", err)
		}
	}
	switch {
	case sealed:
	case opts.KeyFile != "":
		if err := writeKeyFile(opts.KeyFile, key); err != nil {
			return nil, err
		}
		f.info.Source = SourceKeyFile
	default:
		log.Warn("the secrets key isn't kept anywhere; secrets last only as long as this process")
	}
	if err := f.writeKeyCheck(); err != nil {
		return nil, err
	}
	return f, nil
}

func readIfExists(p string) ([]byte, error) {
	raw, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return raw, err
}

// writeKeyFile creates the key file, never replacing one.
func writeKeyFile(p string, key []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("secrets: writing the key file: %w", err)
	}
	_, err = file.Write([]byte(hex.EncodeToString(key) + "\n"))
	if err == nil {
		err = file.Sync()
	}
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(p)
		return fmt.Errorf("secrets: writing the key file: %w", err)
	}
	return nil
}
