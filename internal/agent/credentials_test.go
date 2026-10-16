package agent

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

func TestCredentials(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	in := SealCredentialInput{Name: "racksail-secrets-key", Value: bytes.Repeat([]byte{7}, 32)}

	check, err := SealCredential.Call(ctx, c, in, CallOptions{Check: true})
	if err != nil || !check.Changed || check.Protection != ProtectionTPM2 {
		t.Fatalf("check = %+v, %v", check, err)
	}
	if _, err := b.Files.Stat(ctx, system.CredstoreDir+"/racksail-secrets-key"); !errors.Is(err, system.ErrNotFound) {
		t.Fatalf("check mode wrote the credential: %v", err)
	}
	res, err := SealCredential.Call(ctx, c, in, CallOptions{})
	if err != nil || !res.Changed || res.Protection != ProtectionTPM2 {
		t.Fatalf("seal = %+v, %v", res, err)
	}
	sealed, err := b.Files.ReadFile(ctx, system.CredstoreDir+"/racksail-secrets-key", 0)
	if err != nil || bytes.Contains(sealed, in.Value) {
		t.Fatalf("the credential must be stored sealed: %v", err)
	}
	if info, _ := b.Files.Stat(ctx, system.CredstoreDir); info.Mode.Perm() != 0o700 {
		t.Errorf("credstore mode = %v", info.Mode)
	}
	if res, err := SealCredential.Call(ctx, c, in, CallOptions{}); err != nil || res.Changed {
		t.Errorf("sealing the same value again = %+v, %v", res, err)
	}
	other := SealCredentialInput{Name: in.Name, Value: []byte("another value")}
	if _, err := SealCredential.Call(ctx, c, other, CallOptions{}); !errors.Is(err, ErrConflict) {
		t.Errorf("replacing a credential: %v", err)
	}

	got, err := UnsealCredential.Call(ctx, c, CredentialInput{Name: in.Name}, CallOptions{})
	if err != nil || !bytes.Equal(got.Value, in.Value) {
		t.Fatalf("unseal = %x, %v", got.Value, err)
	}
	if _, err := UnsealCredential.Call(ctx, c, CredentialInput{Name: "racksail-none"}, CallOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing credential: %v", err)
	}
	for _, name := range []string{"secrets-key", "racksail-../x", "racksail-Key"} {
		if _, err := UnsealCredential.Call(ctx, c, CredentialInput{Name: name}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("name %q: %v", name, err)
		}
	}

	// A credential doesn't open on another machine.
	elsewhere := sim.New(sim.Options{Hostname: "elsewhere"})
	if err := elsewhere.Backend().Files.Mkdir(ctx, system.CredstoreDir, 0o700, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	if err := elsewhere.Backend().Files.WriteFile(ctx, system.CredstoreDir+"/racksail-secrets-key", sealed, 0o600, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	if _, err := UnsealCredential.Call(ctx, NewLocal(elsewhere.Backend(), nil), CredentialInput{Name: in.Name}, CallOptions{}); err == nil {
		t.Error("a credential sealed on one machine must not open on another")
	}

	if res, err := RemoveCredential.Call(ctx, c, CredentialInput{Name: in.Name}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("remove = %+v, %v", res, err)
	}
	if res, _ := RemoveCredential.Call(ctx, c, CredentialInput{Name: in.Name}, CallOptions{}); res.Changed {
		t.Error("removing twice changes nothing")
	}
}

func TestCredentialsWithoutTPM(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{Hostname: "no-tpm"})
	m.SetProgram(progSystemdCreds, func(ctx context.Context, m *sim.Machine, cmd system.Command) (system.Output, error) {
		if cmd.Args[0] == "has-tpm2" {
			return system.Output{Stdout: []byte("no\n"), ExitCode: 1}, nil
		}
		if cmd.Args[0] == "encrypt" {
			for _, a := range cmd.Args {
				if a == "--with-key=host+tpm2" {
					return system.Output{ExitCode: 1, Stderr: []byte("TPM2 not supported on this system.\n")}, nil
				}
			}
		}
		return system.Output{Stdout: append([]byte("sealed:"), cmd.Stdin...)}, nil
	})
	res, err := SealCredential.Call(ctx, NewLocal(m.Backend(), nil), SealCredentialInput{Name: "racksail-k", Value: []byte("v")}, CallOptions{})
	if err != nil || res.Protection != ProtectionHost {
		t.Errorf("seal = %+v, %v", res, err)
	}
}

func TestKeySealer(t *testing.T) {
	ctx := context.Background()
	_, c := newLocal(t)
	s := KeySealer{Client: c, Name: "racksail-secrets-key"}
	if key, err := s.Unseal(ctx); key != nil || err != nil {
		t.Fatalf("nothing sealed yet: %x, %v", key, err)
	}
	key := bytes.Repeat([]byte{1}, 32)
	protection, err := s.Seal(ctx, key)
	if err != nil || protection != ProtectionTPM2 {
		t.Fatalf("seal = %q, %v", protection, err)
	}
	got, err := s.Unseal(ctx)
	if err != nil || !bytes.Equal(got, key) {
		t.Errorf("unseal = %x, %v", got, err)
	}
	if _, err := s.Seal(ctx, bytes.Repeat([]byte{2}, 32)); !errors.Is(err, ErrConflict) {
		t.Errorf("sealing another key: %v", err)
	}
}
