//go:build !linux

package linux

import (
	"fmt"
	"runtime"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// statfs is only needed on Linux; elsewhere the adapters are only built for
// their tests, which leave filesystem usage out.
func statfs(p string) (system.FSUsage, error) {
	return system.FSUsage{}, fmt.Errorf("filesystem usage on %s: %w", runtime.GOOS, system.ErrInvalid)
}
