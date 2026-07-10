//go:build !windows

package main

import (
	"path/filepath"
	"syscall"
)

// driveSpace returns (free, total) bytes for the filesystem containing path.
// Non-Windows implementation exists so the Linux CI test build compiles and
// the download logic is unit-testable; production is the Windows build.
func driveSpace(path string) (free uint64, total uint64, err error) {
	p := path
	for {
		var st syscall.Statfs_t
		if e := syscall.Statfs(p, &st); e == nil {
			bs := uint64(st.Bsize)
			return st.Bavail * bs, st.Blocks * bs, nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			var st syscall.Statfs_t
			e := syscall.Statfs(p, &st)
			if e != nil {
				return 0, 0, e
			}
			bs := uint64(st.Bsize)
			return st.Bavail * bs, st.Blocks * bs, nil
		}
		p = parent
	}
}
