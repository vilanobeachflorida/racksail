package system

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"
)

// Exec runs external programs with an argument vector. It is never reachable
// from outside the agent: typed operations use it with fixed program paths
// and validated arguments, and there is no operation that runs an arbitrary
// command.
type Exec interface {
	// Run starts the program and waits for it. A non-zero exit status is
	// returned as an *ExitError together with the output.
	Run(ctx context.Context, cmd Command) (Output, error)
}

// Command is a program to run.
type Command struct {
	// Path is the absolute path of the program, such as /usr/sbin/useradd.
	Path string
	// Args are passed to the program as they are; no shell is involved.
	Args  []string
	Stdin []byte
	// Env adds KEY=value entries to a minimal environment.
	Env []string
	Dir string
	// User runs the program as this user instead of the agent's user.
	User string
	// Timeout stops the program; zero means DefaultTimeout.
	Timeout time.Duration
	// MaxOutput caps the bytes kept from stdout and from stderr; zero means
	// DefaultMaxOutput.
	MaxOutput int
}

// Defaults for Command.
const (
	DefaultTimeout   = 5 * time.Minute
	DefaultMaxOutput = 4 << 20
)

// Output is what a program printed and how it exited.
type Output struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// ExitError reports a program that exited with a non-zero status.
type ExitError struct {
	Program  string
	ExitCode int
	Stderr   string
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if len(msg) > 512 {
		msg = msg[:512] + "..."
	}
	if msg == "" {
		return fmt.Sprintf("%s exited with status %d", e.Program, e.ExitCode)
	}
	return fmt.Sprintf("%s exited with status %d: %s", e.Program, e.ExitCode, msg)
}

// Validate checks that the command names an absolute program and that no
// argument could be misread.
func (c Command) Validate() error {
	if !path.IsAbs(c.Path) || path.Clean(c.Path) != c.Path {
		return fmt.Errorf("program path %q must be absolute: %w", c.Path, ErrInvalid)
	}
	for _, a := range c.Args {
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("argument of %s contains a NUL byte: %w", c.Path, ErrInvalid)
		}
	}
	for _, e := range c.Env {
		if k, _, ok := strings.Cut(e, "="); !ok || k == "" || strings.ContainsRune(e, 0) {
			return fmt.Errorf("environment entry %q: %w", e, ErrInvalid)
		}
	}
	if c.Timeout < 0 || c.MaxOutput < 0 {
		return fmt.Errorf("negative timeout or output limit: %w", ErrInvalid)
	}
	return nil
}

// EffectiveTimeout returns the timeout to apply.
func (c Command) EffectiveTimeout() time.Duration {
	if c.Timeout == 0 {
		return DefaultTimeout
	}
	return c.Timeout
}

// EffectiveMaxOutput returns the output cap to apply.
func (c Command) EffectiveMaxOutput() int {
	if c.MaxOutput == 0 {
		return DefaultMaxOutput
	}
	return c.MaxOutput
}
