package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/vilanobeachflorida/racksail/internal/installer"
)

// setupCodeFile is where the setup code waits for the console's status
// screen, readable by the panel's user only.
const setupCodeFile = "setup-code"

// bootstrap gives the panel a way to its first admin: the account the
// installer left, or a one-time setup code printed here and kept for the
// console until someone uses it, when the identity service removes it.
func (c *core) bootstrap(ctx context.Context, out io.Writer) error {
	state := c.services.Config.Paths.State
	code, err := c.identity.Bootstrap(ctx, filepath.Join(state, installer.BootstrapFile))
	if err != nil {
		return err
	}
	if code == "" {
		return nil
	}
	if err := os.WriteFile(filepath.Join(state, setupCodeFile), []byte(code+"\n"), 0o600); err != nil {
		c.log.Warn("keeping the setup code for the console", "error", err)
	}
	c.log.Warn("the panel has no admin yet; open it and create one with the setup code printed below, or shown on the server's console")
	_, err = fmt.Fprintf(out, "\nNo admin yet. Open the panel and create the first admin with this one-time setup code:\n\n    %s\n\n", code)
	return err
}
