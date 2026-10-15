package linux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"syscall"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// OSRunner runs programs on the local machine with os/exec. Programs get a
// minimal environment and never go through a shell.
type OSRunner struct {
	// Accounts resolves user names for Command.User. When nil, commands that
	// ask for a user fail.
	Accounts func(name string) (uid, gid int, err error)
}

var baseEnv = []string{
	"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"LANG=C.UTF-8",
	"LC_ALL=C.UTF-8",
	"DEBIAN_FRONTEND=noninteractive",
}

func (r *OSRunner) command(ctx context.Context, c system.Command) (*exec.Cmd, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Env = append(append([]string{}, baseEnv...), c.Env...)
	cmd.Dir = c.Dir
	if c.User != "" {
		if r.Accounts == nil {
			return nil, fmt.Errorf("running %s as %s: no account lookup: %w", c.Path, c.User, system.ErrInvalid)
		}
		uid, gid, err := r.Accounts(c.User)
		if err != nil {
			return nil, err
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	}
	return cmd, nil
}

// Run implements system.Exec.
func (r *OSRunner) Run(ctx context.Context, c system.Command) (system.Output, error) {
	ctx, cancel := context.WithTimeout(ctx, c.EffectiveTimeout())
	defer cancel()
	cmd, err := r.command(ctx, c)
	if err != nil {
		return system.Output{}, err
	}
	limit := c.EffectiveMaxOutput()
	stdout := &capped{limit: limit}
	stderr := &capped{limit: limit}
	cmd.Stdin = bytes.NewReader(c.Stdin)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	out := system.Output{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return out, nil
	case errors.As(err, &exitErr):
		out.ExitCode = exitErr.ExitCode()
		return out, &system.ExitError{Program: c.Path, ExitCode: out.ExitCode, Stderr: string(out.Stderr)}
	case ctx.Err() != nil:
		return out, fmt.Errorf("%s: %w", c.Path, ctx.Err())
	default:
		return out, fmt.Errorf("running %s: %w", c.Path, err)
	}
}

// Stream runs a program with its standard input and output connected to in
// and out.
func (r *OSRunner) Stream(ctx context.Context, c system.Command, in io.Reader, out io.Writer) error {
	cmd, err := r.command(ctx, c)
	if err != nil {
		return err
	}
	stderr := &capped{limit: 64 << 10}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &system.ExitError{Program: c.Path, ExitCode: exitErr.ExitCode(), Stderr: stderr.buf.String()}
	}
	if err != nil {
		return fmt.Errorf("running %s: %w", c.Path, err)
	}
	return nil
}

// capped keeps at most limit bytes and silently drops the rest, so a noisy
// program can't exhaust the agent's memory.
type capped struct {
	buf   bytes.Buffer
	limit int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
