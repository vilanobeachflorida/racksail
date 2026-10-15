//go:build linux

package linux

import (
	"syscall"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// statfs reports the space and inodes of the filesystem holding p.
func statfs(p string) (system.FSUsage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return system.FSUsage{}, err
	}
	bsize := int64(st.Bsize)
	return system.FSUsage{
		SizeBytes:   int64(st.Blocks) * bsize,
		UsedBytes:   int64(st.Blocks-st.Bfree) * bsize,
		FreeBytes:   int64(st.Bavail) * bsize,
		InodesTotal: int64(st.Files),
		InodesFree:  int64(st.Ffree),
	}, nil
}
