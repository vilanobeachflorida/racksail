package system

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// Backend is one implementation of every adapter area for one machine.
type Backend struct {
	// Name is "linux" or "sim".
	Name      string
	Facts     Facts
	Users     Users
	Files     Files
	Units     Units
	Packages  Packages
	Btrfs     Btrfs
	Mounts    Mounts
	Disks     Disks
	Firewall  Firewall
	Network   Network
	Sysctl    Sysctl
	Processes Processes
	Certs     Certificates
	Journal   Journal
	Metrics   Metrics
	Exec      Exec
}

// Check reports areas the backend doesn't provide. The agent refuses to start
// with an incomplete backend.
func (b *Backend) Check() error {
	if b == nil {
		return errors.New("system: no backend")
	}
	var missing []string
	v := reflect.ValueOf(b).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() == reflect.Interface && f.IsNil() {
			missing = append(missing, strings.ToLower(t.Field(i).Name))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("system: the %s backend has no adapter for %s", b.Name, strings.Join(missing, ", "))
	}
	return nil
}

// Errors adapters wrap to describe common failures. Wrap them with context:
//
//	fmt.Errorf("user %q: %w", name, system.ErrNotFound)
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrNotEmpty = errors.New("not empty")
	ErrInvalid  = errors.New("invalid argument")
	ErrTooLarge = errors.New("too large")
)

// Owner names the user and group that own a file. Empty fields mean root.
type Owner struct {
	User  string `json:"user,omitempty"`
	Group string `json:"group,omitempty"`
}

// Signal names a process signal without the SIG prefix, such as "TERM".
type Signal string

// Signals the agent may send.
const (
	SIGTERM Signal = "TERM"
	SIGKILL Signal = "KILL"
	SIGHUP  Signal = "HUP"
	SIGINT  Signal = "INT"
	SIGUSR1 Signal = "USR1"
	SIGUSR2 Signal = "USR2"
)

// Valid reports whether s is one of the allowed signals.
func (s Signal) Valid() bool {
	switch s {
	case SIGTERM, SIGKILL, SIGHUP, SIGINT, SIGUSR1, SIGUSR2:
		return true
	}
	return false
}
