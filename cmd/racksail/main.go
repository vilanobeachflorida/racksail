// Command racksail is the single Racksail binary. It runs as the panel, the
// agent, the edge, the installer, a development server or a command-line
// client, depending on the command.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := newRootCommand().ExecuteContext(ctx)
	stop()
	if err != nil {
		var exit exitError
		if errors.As(err, &exit) {
			os.Exit(exit.code)
		}
		fmt.Fprintln(os.Stderr, "racksail:", err)
		os.Exit(1)
	}
}

// exitError ends the program with a specific status after the command has
// already reported the problem.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
