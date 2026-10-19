//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

// driveSpace returns (freeBytesAvailableToCaller, totalBytes) for the volume
// containing path, via GetDiskFreeSpaceExW — stdlib syscall only, no cgo.
// "Free available to caller" is the correct figure (quotas respected), not
// total free sectors.
func driveSpace(path string) (free uint64, total uint64, err error) {
	// The API accepts any directory on the volume; use the path's volume root
	// if the full path does not exist yet (e.g. saving to a new file name).
	p := path
	for {
		if _, statErr := syscall.UTF16PtrFromString(p); statErr != nil {
			return 0, 0, fmt.Errorf("invalid path: %w", statErr)
		}
		ptr, _ := syscall.UTF16PtrFromString(p)
		var freeAvail, totalBytes, totalFree uint64
		r1, _, callErr := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW").Call(
			uintptr(unsafe.Pointer(ptr)),
			uintptr(unsafe.Pointer(&freeAvail)),
			uintptr(unsafe.Pointer(&totalBytes)),
			uintptr(unsafe.Pointer(&totalFree)),
		)
		if r1 != 0 {
			return freeAvail, totalBytes, nil
		}
		// Path may not exist yet — walk up to the parent and retry, stopping
		// at the volume root.
		parent := filepath.Dir(p)
		if parent == p {
			return 0, 0, fmt.Errorf("GetDiskFreeSpaceExW(%s): %v", path, callErr)
		}
		p = parent
	}
}
