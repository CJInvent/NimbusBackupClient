package pbscommon

import (
	"os"
)

// What a directory backup does with one entry, decided before the entry is
// opened (V4-BETA-FIXES §13.2 R1, F-71).
//
// Why this exists: since Go 1.23, os.Lstat and os.ReadDir on Windows report a
// junction or volume mount point as ModeIrregular -- not ModeSymlink, and never
// as a directory -- and every other non-directory reparse point (WOF, cloud
// placeholder, app execution alias) as ModeIrregular too. A writer that skips
// only ModeSymlink therefore opens junctions (following them out of the shadow
// copy to the live volume, or failing on the profile's deny-Everyone legacy
// junctions), app execution aliases (which cannot be opened at all) and
// online-only files (which a sync provider must download first). The mode bits
// cannot tell these apart; the attributes and the reparse tag can.

// EntryDisposition is what the backup does with one directory entry.
type EntryDisposition int

const (
	// ArchiveEntry: read and archive it (regular files, directories, and
	// reparse points whose filter supplies the data: WOF, dedup, a cloud file
	// that is on this device).
	ArchiveEntry EntryDisposition = iota
	// SkipLink: a junction, volume mount point or symbolic link. Never followed.
	SkipLink
	// SkipOnlineOnly: a placeholder whose data is not on this device (cloud
	// sync or tiered storage). Not opened, so nothing is downloaded.
	SkipOnlineOnly
	// SkipNotData: an app execution alias or AF_UNIX socket. Nothing to read.
	SkipNotData
)

// Windows attribute bits and reparse tags (winnt.h). Plain constants so the
// rule table below is testable on every platform.
const (
	fileAttributeDirectory          = 0x00000010
	fileAttributeReparsePoint       = 0x00000400
	fileAttributeOffline            = 0x00001000
	fileAttributeRecallOnOpen       = 0x00040000
	fileAttributeRecallOnDataAccess = 0x00400000

	reparseTagNameSurrogate = 0x20000000
	ioReparseTagSymlink     = 0xA000000C
	ioReparseTagMountPoint  = 0xA0000003
	ioReparseTagAppExecLink = 0x8000001B
	ioReparseTagAFUnix      = 0x80000023
)

// classifyAttrs is the R1 rule table over raw attributes and reparse tag. The
// tag is meaningful only when the reparse-point attribute is set.
func classifyAttrs(attrs, tag uint32) EntryDisposition {
	reparse := attrs&fileAttributeReparsePoint != 0
	if reparse {
		if tag&reparseTagNameSurrogate != 0 {
			return SkipLink
		}
		switch tag {
		case ioReparseTagAppExecLink, ioReparseTagAFUnix:
			return SkipNotData
		}
	}
	// A placeholder directory is traversed: what is on the device is backed
	// up, and listing it downloads no file content.
	if attrs&fileAttributeDirectory != 0 {
		return ArchiveEntry
	}
	// RECALL_ON_* are set only by a cloud or tiering filter. OFFLINE alone is
	// an attribute anyone can set and moves no data; with a reparse point it
	// marks tiered storage whose provider must fetch the data.
	if attrs&(fileAttributeRecallOnDataAccess|fileAttributeRecallOnOpen) != 0 {
		return SkipOnlineOnly
	}
	if reparse && attrs&fileAttributeOffline != 0 {
		return SkipOnlineOnly
	}
	return ArchiveEntry
}

// entryAttrsFunc returns an entry's Windows attributes and, when it is a
// reparse point, its tag. The platform implementation is platformEntryAttrs;
// tests inject their own through PXARArchive.entryAttrs.
type entryAttrsFunc func(path string, fi os.FileInfo) (attrs uint32, tag uint32, err error)

// ClassifyEntry applies R1 to a path already Lstat'ed. A non-nil error means
// the entry is a reparse point whose tag could not be read; the caller records
// it as a read error and skips it.
func ClassifyEntry(path string, fi os.FileInfo) (EntryDisposition, error) {
	return classifyWith(platformEntryAttrs, path, fi)
}

func classifyWith(get entryAttrsFunc, path string, fi os.FileInfo) (EntryDisposition, error) {
	attrs, tag, err := get(path, fi)
	if err != nil {
		return ArchiveEntry, err
	}
	return classifyAttrs(attrs, tag), nil
}
