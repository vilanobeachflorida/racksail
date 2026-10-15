package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Credentials are values sealed with systemd-creds: with the TPM2 chip and
// the host key when the machine has a TPM2, with the host key alone
// otherwise. A sealed credential opens only on the machine that sealed it,
// for root or for a service that imports it at start, as the panel imports
// the key its secrets are encrypted with.

const progSystemdCreds = "/usr/bin/systemd-creds"

var credentialNamePattern = regexp.MustCompile(`^racksail-[a-z0-9][a-z0-9-]{0,62}$`)

func checkCredentialName(name string) error {
	if !credentialNamePattern.MatchString(name) {
		return fmt.Errorf("credential name %q must start with racksail- and use lowercase letters, digits and dashes", name)
	}
	return nil
}

func credentialPath(name string) string { return path.Join(system.CredstoreDir, name) }

// Protection levels of sealed credentials.
const (
	ProtectionTPM2 = "tpm2+host"
	ProtectionHost = "host"
)

// hasTPM2 asks systemd-creds whether it can seal with a TPM2, which needs
// the chip, its driver and systemd's support for it.
func hasTPM2(ctx context.Context, env *Env) (bool, error) {
	_, err := env.System.Exec.Run(ctx, system.Command{Path: progSystemdCreds, Args: []string{"has-tpm2", "--quiet"}})
	var exit *system.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit):
		return false, nil
	}
	return false, err
}

// unsealCredential returns a sealed credential's value, ErrNotFound when
// there is none.
func unsealCredential(ctx context.Context, env *Env, name string) ([]byte, error) {
	sealed, err := env.System.Files.ReadFile(ctx, credentialPath(name), 1<<20)
	if err != nil {
		return nil, err
	}
	out, err := env.System.Exec.Run(ctx, system.Command{
		Path: progSystemdCreds, Args: []string{"decrypt", "--name=" + name, "-", "-"}, Stdin: sealed,
	})
	if err != nil {
		return nil, fmt.Errorf("credential %s can't be opened on this machine, perhaps because its TPM was reset: %w", name, err)
	}
	return out.Stdout, nil
}

// SealCredentialInput is a value to seal.
type SealCredentialInput struct {
	Name  string `json:"name" doc:"Credential name, starting with racksail-"`
	Value []byte `json:"value" doc:"The value to seal"`
}

// Validate implements Input.
func (in SealCredentialInput) Validate() error {
	if err := checkCredentialName(in.Name); err != nil {
		return err
	}
	if len(in.Value) == 0 || len(in.Value) > 64<<10 {
		return fmt.Errorf("a credential holds between 1 byte and 64 KiB")
	}
	return nil
}

// SealResult says how a credential is sealed.
type SealResult struct {
	Result
	// Protection is tpm2+host or host; empty when the credential was
	// already sealed.
	Protection string `json:"protection,omitempty" enum:"tpm2+host,host" doc:"What the credential is sealed with"`
}

// SealCredential seals a value into /etc/credstore.encrypted, where
// services import it with ImportCredential=. A credential holding another
// value is never replaced, since what was encrypted with it would be lost.
var SealCredential = Register(Def[SealCredentialInput, SealResult]{
	Name:    "SealCredential",
	Summary: "Seal a credential with systemd-creds",
	Description: "Encrypts a value with systemd-creds, bound to the machine's TPM2 chip when it has one and to the host key always, " +
		"and keeps it in /etc/credstore.encrypted for services that import it. Sealing the same value again changes nothing; " +
		"a credential that holds another value is refused.",
	Sensitive: true,
	Effects: func(in SealCredentialInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFile, Action: "write", Target: credentialPath(in.Name)}}
	},
	Run: func(ctx context.Context, env *Env, in SealCredentialInput) (SealResult, error) {
		current, err := unsealCredential(ctx, env, in.Name)
		switch {
		case err == nil && bytes.Equal(current, in.Value):
			return SealResult{Result: Unchanged()}, nil
		case err == nil:
			return SealResult{}, fmt.Errorf("credential %s already holds another value: %w", in.Name, system.ErrExists)
		case !errors.Is(err, system.ErrNotFound):
			return SealResult{}, err
		}
		tpm, err := hasTPM2(ctx, env)
		if err != nil {
			return SealResult{}, err
		}
		out := SealResult{Protection: ProtectionHost}
		if tpm {
			out.Protection = ProtectionTPM2
		}
		out.Result = Changed(system.Effect{Type: system.EffectFile, Action: "write", Target: credentialPath(in.Name), Detail: out.Protection})
		if env.Check {
			return out, nil
		}
		// No PCRs: a firmware or Secure Boot update must not lock the panel
		// out of its own secrets. The chip itself still has to be there.
		withKey := "host"
		if tpm {
			withKey = "host+tpm2"
		}
		sealed, err := env.System.Exec.Run(ctx, system.Command{
			Path:  progSystemdCreds,
			Args:  []string{"encrypt", "--name=" + in.Name, "--with-key=" + withKey, "--tpm2-pcrs=", "-", "-"},
			Stdin: in.Value,
		})
		if err != nil {
			return SealResult{}, err
		}
		files := env.System.Files
		if _, err := files.Stat(ctx, system.CredstoreDir); errors.Is(err, system.ErrNotFound) {
			if err := files.Mkdir(ctx, system.CredstoreDir, 0o700, system.Owner{}); err != nil && !errors.Is(err, system.ErrExists) {
				return SealResult{}, err
			}
		} else if err != nil {
			return SealResult{}, err
		}
		if err := files.WriteFile(ctx, credentialPath(in.Name), sealed.Stdout, 0o600, system.Owner{}); err != nil {
			return SealResult{}, err
		}
		return out, nil
	},
})

// CredentialInput names a credential.
type CredentialInput struct {
	Name string `json:"name" doc:"Credential name, starting with racksail-"`
}

// Validate implements Input.
func (in CredentialInput) Validate() error { return checkCredentialName(in.Name) }

// CredentialValue is an unsealed credential.
type CredentialValue struct {
	Value []byte `json:"value"`
}

// UnsealCredential opens a sealed credential, for a service that was
// started without systemd importing it, such as the panel in development.
var UnsealCredential = Register(Def[CredentialInput, CredentialValue]{
	Name:      "UnsealCredential",
	Summary:   "Open a sealed credential",
	ReadOnly:  true,
	Sensitive: true,
	Run: func(ctx context.Context, env *Env, in CredentialInput) (CredentialValue, error) {
		v, err := unsealCredential(ctx, env, in.Name)
		return CredentialValue{Value: v}, err
	},
})

// RemoveCredential removes a sealed credential, such as one that can no
// longer be opened after the TPM was reset.
var RemoveCredential = Register(Def[CredentialInput, Result]{
	Name:    "RemoveCredential",
	Summary: "Remove a sealed credential",
	Effects: func(in CredentialInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFile, Action: "delete", Target: credentialPath(in.Name)}}
	},
	Run: func(ctx context.Context, env *Env, in CredentialInput) (Result, error) {
		p := credentialPath(in.Name)
		if _, err := env.System.Files.Stat(ctx, p); errors.Is(err, system.ErrNotFound) {
			return Unchanged(), nil
		} else if err != nil {
			return Result{}, err
		}
		effect := system.Effect{Type: system.EffectFile, Action: "delete", Target: p}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Files.Remove(ctx, p)
	},
})

// KeySealer seals a key as a credential through an agent: the panel's
// secrets.Sealer, with systemd-creds on a server and the simulated machine
// in development.
type KeySealer struct {
	Client Client
	// Name is the credential, such as racksail-secrets-key.
	Name string
}

// Seal seals key, unless the credential already holds it.
func (s KeySealer) Seal(ctx context.Context, key []byte) (string, error) {
	res, err := SealCredential.Call(ctx, s.Client, SealCredentialInput{Name: s.Name, Value: key}, CallOptions{})
	if err != nil {
		return "", err
	}
	return res.Protection, nil
}

// Unseal returns the credential's value, or nil when it doesn't exist.
func (s KeySealer) Unseal(ctx context.Context) ([]byte, error) {
	v, err := UnsealCredential.Call(ctx, s.Client, CredentialInput{Name: s.Name}, CallOptions{})
	if ErrNotFound.Is(err) {
		return nil, nil
	}
	return v.Value, err
}
