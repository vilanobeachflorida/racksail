//go:build linux

package edge

import (
	"os"
	"strconv"
)

// openedPath returns where an open file is, as the kernel resolved it when
// it was opened, whatever links led there.
func openedPath(f *os.File) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(f.Fd()), 10))
}
