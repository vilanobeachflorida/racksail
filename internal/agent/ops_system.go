package agent

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// GetFacts reports what the machine is.
var GetFacts = Register(Def[Empty, system.MachineFacts]{
	Name:     "GetFacts",
	Summary:  "Describe the machine",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, _ Empty) (system.MachineFacts, error) {
		return env.System.Facts.Facts(ctx)
	},
})

// sysctlPrefixes are the kernel parameters Racksail tunes.
var sysctlPrefixes = []string{"net.", "vm.", "fs.inotify.", "fs.file-max", "kernel.pid_max", "kernel.threads-max"}

var sysctlNamePattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// SysctlInput is a group of kernel parameters kept in one file.
type SysctlInput struct {
	// Name names the file /etc/sysctl.d/90-racksail-<name>.conf.
	Name   string            `json:"name" doc:"Name of the group, such as network"`
	Values map[string]string `json:"values" doc:"Parameters and values"`
}

// Validate implements Input.
func (in SysctlInput) Validate() error {
	if !sysctlNamePattern.MatchString(in.Name) {
		return fmt.Errorf("sysctl group name %q", in.Name)
	}
	if len(in.Values) == 0 {
		return fmt.Errorf("no parameters given")
	}
	for k, v := range in.Values {
		if !system.ValidSysctlKey(k) || !slices.ContainsFunc(sysctlPrefixes, func(p string) bool { return strings.HasPrefix(k, p) }) {
			return fmt.Errorf("parameter %s isn't one Racksail tunes", k)
		}
		if strings.ContainsAny(v, "\n\x00") || len(v) > 256 {
			return fmt.Errorf("value of %s", k)
		}
	}
	return nil
}

// SetSysctl sets kernel parameters now and keeps them across reboots.
var SetSysctl = Register(Def[SysctlInput, Result]{
	Name:    "SetSysctl",
	Summary: "Tune kernel parameters",
	Effects: func(in SysctlInput) []system.Effect {
		var out []system.Effect
		for _, k := range slices.Sorted(maps.Keys(in.Values)) {
			out = append(out, system.Effect{Type: system.EffectKernel, Action: "set", Target: k, Detail: in.Values[k]})
		}
		return out
	},
	Run: func(ctx context.Context, env *Env, in SysctlInput) (Result, error) {
		keys := slices.Sorted(maps.Keys(in.Values))
		var file bytes.Buffer
		file.WriteString(system.GeneratedHeader + "\n")
		var effects []system.Effect
		var todo []string
		for _, k := range keys {
			fmt.Fprintf(&file, "%s = %s\n", k, in.Values[k])
			if cur, err := env.System.Sysctl.Get(ctx, k); err != nil || cur != in.Values[k] {
				todo = append(todo, k)
				effects = append(effects, system.Effect{Type: system.EffectKernel, Action: "set", Target: k, Detail: in.Values[k]})
			}
		}
		path := "/etc/sysctl.d/90-racksail-" + in.Name + ".conf"
		current, err := env.System.Files.ReadFile(ctx, path, 1<<20)
		fileChanged := err != nil || !bytes.Equal(current, file.Bytes())
		if fileChanged {
			effects = append(effects, system.Effect{Type: system.EffectFile, Action: "write", Target: path})
		}
		if len(effects) == 0 {
			return Unchanged(), nil
		}
		if env.Check {
			return Changed(effects...), nil
		}
		for _, k := range todo {
			if err := env.System.Sysctl.Set(ctx, k, in.Values[k]); err != nil {
				return Result{}, err
			}
		}
		if fileChanged {
			if err := env.System.Files.WriteFile(ctx, path, file.Bytes(), 0o644, system.Owner{}); err != nil {
				return Result{}, err
			}
		}
		return Changed(effects...), nil
	},
})

// ProcessFilterInput narrows a process list.
type ProcessFilterInput struct {
	Filter system.ProcessFilter `json:"filter,omitzero"`
}

// Validate implements Input.
func (in ProcessFilterInput) Validate() error {
	if in.Filter.User != "" {
		if err := checkAccountName("user", in.Filter.User); err != nil {
			return err
		}
	}
	if in.Filter.Unit != "" && !system.ValidUnitName(in.Filter.Unit) {
		return fmt.Errorf("%q is not a unit name", in.Filter.Unit)
	}
	return nil
}

// ProcessList lists processes.
type ProcessList struct {
	Processes []system.Process `json:"processes"`
}

// ListProcesses lists processes, for example those of one account.
var ListProcesses = Register(Def[ProcessFilterInput, ProcessList]{
	Name:     "ListProcesses",
	Summary:  "List processes",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, in ProcessFilterInput) (ProcessList, error) {
		procs, err := env.System.Processes.List(ctx, in.Filter)
		if procs == nil {
			procs = []system.Process{}
		}
		return ProcessList{Processes: procs}, err
	},
})

// SignalInput sends a signal to a process.
type SignalInput struct {
	PID    int           `json:"pid" doc:"Process ID"`
	Signal system.Signal `json:"signal" enum:"TERM,KILL,HUP,INT,USR1,USR2"`
}

// Validate implements Input.
func (in SignalInput) Validate() error {
	if in.PID <= 1 {
		return fmt.Errorf("pid %d can't be signalled", in.PID)
	}
	if !in.Signal.Valid() {
		return fmt.Errorf("signal %q isn't allowed", in.Signal)
	}
	return nil
}

// SignalProcess signals a process that belongs to an account or to a
// Racksail unit. Other processes are refused.
var SignalProcess = Register(Def[SignalInput, Result]{
	Name:    "SignalProcess",
	Summary: "Send a signal to a process",
	Effects: func(in SignalInput) []system.Effect {
		return []system.Effect{{Type: system.EffectProcess, Action: "signal", Target: fmt.Sprint(in.PID), Detail: string(in.Signal)}}
	},
	Run: func(ctx context.Context, env *Env, in SignalInput) (Result, error) {
		procs, err := env.System.Processes.List(ctx, system.ProcessFilter{})
		if err != nil {
			return Result{}, err
		}
		i := slices.IndexFunc(procs, func(p system.Process) bool { return p.PID == in.PID })
		if i < 0 {
			return Unchanged(), nil
		}
		p := procs[i]
		account := p.UID >= system.AccountUIDMin && p.UID <= system.AccountUIDMax
		if !account && !strings.HasPrefix(p.Unit, "racksail-") {
			return Result{}, ErrNotAllowed.New("op", "SignalProcess", "target", fmt.Sprintf("process %d (%s)", p.PID, p.Command))
		}
		effect := system.Effect{Type: system.EffectProcess, Action: "signal", Target: fmt.Sprint(in.PID), Detail: string(in.Signal)}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Processes.Signal(ctx, in.PID, in.Signal)
	},
})

// CertificateInput is a certificate for services that read it from disk.
type CertificateInput struct {
	Name  string `json:"name" doc:"Certificate name, such as mail"`
	Chain []byte `json:"chain" doc:"PEM certificate chain, leaf first"`
	Key   []byte `json:"key" doc:"PEM private key"`
	// Group may read the key, such as the group of the mail server.
	Group string `json:"group,omitempty" doc:"Group allowed to read the key"`
}

// Validate implements Input.
func (in CertificateInput) Validate() error {
	if !system.ValidCertName(in.Name) {
		return fmt.Errorf("certificate name %q", in.Name)
	}
	if len(in.Chain) == 0 || len(in.Chain) > 1<<20 || len(in.Key) == 0 || len(in.Key) > 64<<10 {
		return fmt.Errorf("certificate %s needs a chain and a key of sensible size", in.Name)
	}
	if in.Group != "" {
		return checkAccountName("group", in.Group)
	}
	return nil
}

// InstallCertificate installs a certificate and key under
// /etc/racksail/certs/<name>.
var InstallCertificate = Register(Def[CertificateInput, Result]{
	Name:    "InstallCertificate",
	Summary: "Install a certificate",
	Effects: func(in CertificateInput) []system.Effect {
		return []system.Effect{{Type: system.EffectCertificate, Action: "install", Target: in.Name}}
	},
	Run: func(ctx context.Context, env *Env, in CertificateInput) (Result, error) {
		want, err := system.ParseCertInfo(in.Name, in.Chain)
		if err != nil {
			return Result{}, err
		}
		if cur, err := env.System.Certs.Get(ctx, in.Name); err == nil && cur.FingerprintSHA256 == want.FingerprintSHA256 {
			return Unchanged(), nil
		}
		effect := system.Effect{Type: system.EffectCertificate, Action: "install", Target: in.Name, Detail: strings.Join(want.DNSNames, ", ")}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Certs.Install(ctx, in.Name, in.Chain, in.Key, system.Owner{Group: in.Group})
	},
})

// CertNameInput names a certificate.
type CertNameInput struct {
	Name string `json:"name" doc:"Certificate name"`
}

// Validate implements Input.
func (in CertNameInput) Validate() error {
	if !system.ValidCertName(in.Name) {
		return fmt.Errorf("certificate name %q", in.Name)
	}
	return nil
}

// RemoveCertificate removes an installed certificate.
var RemoveCertificate = Register(Def[CertNameInput, Result]{
	Name:    "RemoveCertificate",
	Summary: "Remove a certificate",
	Effects: func(in CertNameInput) []system.Effect {
		return []system.Effect{{Type: system.EffectCertificate, Action: "remove", Target: in.Name}}
	},
	Run: func(ctx context.Context, env *Env, in CertNameInput) (Result, error) {
		if _, err := env.System.Certs.Get(ctx, in.Name); err != nil {
			return Unchanged(), nil
		}
		effect := system.Effect{Type: system.EffectCertificate, Action: "remove", Target: in.Name}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Certs.Remove(ctx, in.Name)
	},
})

// CertificateList lists certificates.
type CertificateList struct {
	Certificates []system.CertInfo `json:"certificates"`
}

// ListCertificates lists installed certificates with their expiry.
var ListCertificates = Register(Def[Empty, CertificateList]{
	Name:     "ListCertificates",
	Summary:  "List certificates",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, _ Empty) (CertificateList, error) {
		certs, err := env.System.Certs.List(ctx)
		if certs == nil {
			certs = []system.CertInfo{}
		}
		return CertificateList{Certificates: certs}, err
	},
})
