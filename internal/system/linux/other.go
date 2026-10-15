//go:build !linux

package linux

import (
	"context"
	"fmt"
	"runtime"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// New fails on operating systems other than Linux, where the simulated
// backend is the one to use.
func New(ctx context.Context) (*system.Backend, error) {
	return nil, fmt.Errorf("the linux backend only runs on Linux, not %s; use the simulated server instead", runtime.GOOS)
}
