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

// logicalDriveRoots enumerates mounted drive roots ("C:\", "D:\", ...) via
// GetLogicalDrives. Pure syscall — no COM, no shell APIs. The in-app path
// picker is built on this instead of the native folder dialog, which faults
// natively in ways recover() cannot catch.
func logicalDriveRoots() []string {
	r1, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetLogicalDrives").Call()
	mask := uint32(r1)
	var out []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, string(rune('A'+i))+`:\`)
		}
	}
	return out
}

// volumeLabel returns the volume label for a drive root, or "" if unavailable.
func volumeLabel(root string) string {
	ptr, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return ""
	}
	name := make([]uint16, 261)
	r1, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetVolumeInformationW").Call(
		uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)),
		0, 0, 0, 0, 0,
	)
	if r1 == 0 {
		return ""
	}
	return syscall.UTF16ToString(name)
}
