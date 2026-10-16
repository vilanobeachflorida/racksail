package addons

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// What turned safe mode on.
const (
	// SafeModeBoot is the boot option, racksail.safe_mode on the kernel's
	// command line, chosen from the boot menu. It lasts until the server
	// boots without it.
	SafeModeBoot = "boot"
	// SafeModeConsole is the console menu, or racksail safe-mode on.
	SafeModeConsole = "console"
	// SafeModeAutomatic is the panel itself, when an add-on kept crashing
	// as it started.
	SafeModeAutomatic = "automatic"
)

// BootOption is the kernel command line word that starts the server in
// safe mode.
const BootOption = "racksail.safe_mode"

// Flag files in the panel's state directory.
const (
	consoleFlag   = "safe-mode"
	automaticFlag = "safe-mode.auto"
)

// SafeModeSource is one reason safe mode is on.
type SafeModeSource struct {
	Kind   string    `json:"kind" enum:"boot,console,automatic" doc:"What turned safe mode on"`
	Reason string    `json:"reason,omitempty" doc:"Why, in plain words"`
	Since  time.Time `json:"since,omitzero" doc:"When it was turned on"`
}

// SafeModeStatus says whether safe mode is on and why.
type SafeModeStatus struct {
	Active  bool             `json:"active" doc:"Whether safe mode is on, so no add-on runs"`
	Sources []SafeModeSource `json:"sources,omitempty" doc:"What turned it on; it stays on until every one is gone"`
}

// SafeMode says whether add-ons may run. It is on while any of its
// sources is: the boot option, the console's flag or the panel's own,
// kept as files so they last until someone turns them off.
type SafeMode struct {
	dir string
	mu  sync.Mutex
	// boot is read once, when the panel starts.
	boot bool
}

// OpenSafeMode reads the boot option from cmdline, the kernel's command
// line, and keeps its flags in dir, the panel's state directory.
func OpenSafeMode(dir string, cmdline []byte) *SafeMode {
	s := &SafeMode{dir: dir}
	for _, word := range strings.Fields(string(cmdline)) {
		if word == BootOption || word == BootOption+"=1" || word == BootOption+"=yes" {
			s.boot = true
		}
	}
	return s
}

// ReadCmdline returns the kernel's command line, or nothing where there
// is none to read, as on a developer's machine.
func ReadCmdline() []byte {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return nil
	}
	return data
}

// ConsoleFlag is the file racksail safe-mode on writes.
func ConsoleFlag(stateDir string) string { return filepath.Join(stateDir, consoleFlag) }

type flagFile struct {
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
}

func (s *SafeMode) read(name string) (*flagFile, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f flagFile
	if json.Unmarshal(data, &f) != nil {
		// A flag written by hand still counts.
		f.Reason = strings.TrimSpace(string(data))
	}
	return &f, nil
}

// Status says whether safe mode is on and why.
func (s *SafeMode) Status() SafeModeStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st SafeModeStatus
	if s.boot {
		st.Sources = append(st.Sources, SafeModeSource{Kind: SafeModeBoot, Reason: "The server was started with the " + BootOption + " boot option."})
	}
	for _, f := range []struct{ kind, name string }{{SafeModeConsole, consoleFlag}, {SafeModeAutomatic, automaticFlag}} {
		if fl, err := s.read(f.name); fl != nil || err != nil {
			src := SafeModeSource{Kind: f.kind}
			if fl != nil {
				src.Reason, src.Since = fl.Reason, fl.Since
			}
			st.Sources = append(st.Sources, src)
		}
	}
	st.Active = len(st.Sources) > 0
	return st
}

// Active reports whether safe mode is on.
func (s *SafeMode) Active() bool { return s.Status().Active }

// Enter turns safe mode on from the console or on the panel's own.
func (s *SafeMode) Enter(kind, reason string, now time.Time) error {
	name := consoleFlag
	if kind == SafeModeAutomatic {
		name = automaticFlag
	}
	return WriteFlag(filepath.Join(s.dir, name), reason, now)
}

// WriteFlag writes a safe mode flag file, as the console command does.
func WriteFlag(path, reason string, now time.Time) error {
	data, err := json.Marshal(flagFile{Reason: reason, Since: now.UTC()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Leave turns off the console's and the panel's own safe mode. The boot
// option lasts until the server boots without it.
func (s *SafeMode) Leave() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, name := range []string{consoleFlag, automaticFlag} {
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
