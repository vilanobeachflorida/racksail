//go:build linux

package linux

import (
	"context"
	"net/http"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// New returns the backend for the machine the agent runs on.
func New(ctx context.Context) (*system.Backend, error) {
	sd, err := DialSystemd(ctx)
	if err != nil {
		return nil, err
	}
	return NewWith(Options{
		Root:    "/",
		Runner:  &OSRunner{Accounts: AccountIDs("/")},
		Systemd: sd,
		HTTP:    &http.Client{Timeout: 30 * time.Minute},
	}), nil
}
