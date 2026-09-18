//go:build windows

package main

import "unicode/utf16"

// volumeMountPaths decodes the bounded MULTI_SZ returned by Windows. Scan the
// UTF-16 buffer itself; the disk-extents byte buffer is unrelated to its length.
func volumeMountPaths(buf []uint16) []string {
	out := make([]string, 0)
	for start := 0; start < len(buf) && buf[start] != 0; {
		end := start
		for end < len(buf) && buf[end] != 0 {
			end++
		}
		if end == len(buf) {
			break
		} // incomplete path is not a valid mount
		out = append(out, string(utf16.Decode(buf[start:end])))
		start = end + 1
	}
	return out
}
