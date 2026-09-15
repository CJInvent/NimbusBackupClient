package main

import (
	"fmt"
	"sort"
	"strings"
)

// Resolving what an operator MEANS by a disk into what the engine can open.
//
// THE BUG THIS FIXES, verbatim: a managed job created in the portal with the
// drive "C" failed at run time with `invalid physical drive path: C`. The
// portal's field was labelled "Drives (one per line)" and placeholdered "C",
// the field arrived as ScheduledJob.DriveLetters, and the image engine reads
// its targets expecting `\\.\PhysicalDriveN`. Nothing in between translated,
// so the two ends had disagreed since managed jobs shipped and the first
// image job anybody created through the portal was guaranteed to fail.
//
// WHY THE SERVER SHOULD KEEP SPEAKING IN LETTERS. The obvious repair is to
// make the portal collect `\\.\PhysicalDrive0` instead. That is worse:
//
//   - A device path is a fact about hardware AT ONE MOMENT. Disk numbering
//     moves when a disk is added, removed, or enumerated in a different
//     order after a firmware update, so a job pinned to PhysicalDrive0 can
//     quietly start imaging the wrong disk -- a backup that succeeds loudly
//     and protects nothing.
//   - "C" is what the operator means, and it is stable across exactly the
//     events that move disk numbers.
//
// So the letter travels, and THE MACHINE resolves it at run time, because the
// machine is the only party that knows which spindle carries C: right now.
// Device paths are still accepted verbatim, for the agent's own GUI picker
// which has a real disk in hand and no letter to offer for an unlettered one.

// resolveDiskTargets maps job targets onto raw device paths.
//
// Accepts, per entry: a drive letter ("C", "c:", "C:\"), or a device path
// (`\\.\PhysicalDriveN`) which passes through untouched.
//
// Returns the device paths in the order the disks were named, deduplicated:
// two letters on one disk are one image, not two. That is not a nicety -- the
// engine would otherwise back up the same spindle twice into one snapshot.
func resolveDiskTargets(targets []string, disks []PhysicalDiskInfo) ([]string, error) {
	byLetter := make(map[string]PhysicalDiskInfo, len(disks))
	for _, d := range disks {
		for _, l := range d.Letters {
			byLetter[normalizeDriveLetter(l)] = d
		}
	}

	out := make([]string, 0, len(targets))
	seen := make(map[string]bool, len(targets))
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}

		if strings.HasPrefix(t, `\\.\PhysicalDrive`) {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
			continue
		}

		letter := normalizeDriveLetter(t)
		if letter == "" {
			return nil, fmt.Errorf("%s: %q is neither a drive letter nor a device path",
				errDiskUnresolved, t)
		}
		disk, ok := byLetter[letter]
		if !ok {
			// Name what IS here. "C is not a drive on this machine" sends a
			// tech looking for a missing disk; listing what the machine has
			// usually shows them the answer -- a letter that moved, or a job
			// pointed at a machine it was never meant for.
			return nil, fmt.Errorf("%s: drive %s: is not on this machine (it has: %s)",
				errDiskUnresolved, letter, describeDisks(disks))
		}
		if !seen[disk.Path] {
			seen[disk.Path] = true
			out = append(out, disk.Path)
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("%s", errDiskRequired)
	}
	return out, nil
}

// normalizeDriveLetter reduces "c", "C:", "C:\" to "C", and returns "" for
// anything that is not a single-letter drive.
func normalizeDriveLetter(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, `\`)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ":")
	if len(s) != 1 {
		return ""
	}
	c := s[0]
	if c >= 'a' && c <= 'z' {
		c -= 'a' - 'A'
	}
	if c < 'A' || c > 'Z' {
		return ""
	}
	return string(c)
}

// describeDisks renders the machine's disks for an error message.
func describeDisks(disks []PhysicalDiskInfo) string {
	if len(disks) == 0 {
		return "no disks it can read"
	}
	parts := make([]string, 0, len(disks))
	for _, d := range disks {
		letters := append([]string(nil), d.Letters...)
		sort.Strings(letters)
		if len(letters) == 0 {
			parts = append(parts, fmt.Sprintf("disk %d (no drive letters)", d.DiskNumber))
			continue
		}
		parts = append(parts, fmt.Sprintf("disk %d (%s)", d.DiskNumber, strings.Join(letters, ", ")))
	}
	return strings.Join(parts, "; ")
}
