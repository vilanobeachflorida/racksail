//go:build !linux

package edge

import (
	"os"
	"path/filepath"
)

// openedPath returns where an open file is. Outside Linux, where the edge
// only runs for development, the name is resolved again.
func openedPath(f *os.File) (string, error) {
	return filepath.EvalSymlinks(f.Name())
}
