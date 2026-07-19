package security

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Path safety for UNTRUSTED path fragments.
//
// Restore paths do not come from the user — they come from the backup: PXAR
// entry names, or filenames parsed out of a backup image's $MFT / FAT
// directory entries. Nothing in those on-disk formats prevents a name from
// containing a path separator or a ".." component. It is the Windows *API*
// that forbids them, not the format, so a crafted or corrupted image can
// carry a name like `..\..\..\Windows\System32\evil.dll`.
//
// filepath.Join CLEANS its result, which RESOLVES ".." rather than rejecting
// it, so `filepath.Join(dest, "../../x")` silently lands outside dest. The
// service performs restores as LocalSystem, so an escape there is an
// arbitrary write as SYSTEM. Every join of untrusted path data must go
// through SafeJoin.
//
// ValidatePath above is a different tool for a different job: it checks
// paths the USER typed, and its `strings.Contains(p, "..")` test would reject
// a legitimate file called "notes..txt" while still saying nothing about
// absolute paths or drive letters.

// IsUnsafeRelPath reports whether p, taken from untrusted backup data, could
// escape the directory it is about to be joined to.
//
// Refused: empty paths, embedded NUL, anything absolute (unix `/`, UNC
// `\\server\share`), anything drive-qualified (`C:`, `C:/x`, and the
// drive-RELATIVE `C:x`, which resolves against that drive's working
// directory), and any `..` component.
//
// Separator handling is deliberately OS-independent: backslashes are folded
// to forward slashes before inspection, so the same name is judged identically
// whether the check runs on the Windows agent or a Linux CI runner. A guard
// that only rejects traversal on the platform it happens to be compiled for is
// not a guard.
func IsUnsafeRelPath(p string) bool {
	if p == "" {
		return true
	}
	if strings.ContainsRune(p, 0) {
		return true
	}
	q := strings.ReplaceAll(p, `\`, "/")
	if strings.HasPrefix(q, "/") {
		// Unix-absolute, and UNC (`\\server\share` folds to `//server/share`).
		return true
	}
	if len(q) >= 2 && q[1] == ':' {
		// Drive-absolute (C:/x) and drive-relative (C:x) alike.
		return true
	}
	for _, seg := range strings.Split(q, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// SafeJoin places an untrusted relative path beneath root, or fails.
//
// Two independent checks, because one can be wrong: the syntactic screen in
// IsUnsafeRelPath, then a containment proof on the cleaned result. The second
// is what catches whatever the first did not anticipate.
func SafeJoin(root, rel string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("safe join: root directory required")
	}
	if IsUnsafeRelPath(rel) {
		return "", fmt.Errorf("unsafe path refused (possible traversal): %q", rel)
	}

	// Fold separators first so a Windows-style name is decomposed into real
	// components on every OS, then let the host form its native path.
	joined := filepath.Join(root, filepath.FromSlash(strings.ReplaceAll(rel, `\`, "/")))

	cleanRoot := filepath.Clean(root)
	if joined != cleanRoot && !strings.HasPrefix(joined, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe path refused (escapes destination): %q", rel)
	}
	return joined, nil
}

// SafeBaseName returns the final component of an untrusted path for use in a
// flattened restore, or fails if that component is not a usable file name.
//
// filepath.Base is not sufficient on its own: Base("..") is "..", and joining
// that to a destination yields its PARENT. Flattening does not make a hostile
// name safe.
func SafeBaseName(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("unsafe file name refused: %q", p)
	}
	q := strings.ReplaceAll(p, `\`, "/")
	base := q
	if i := strings.LastIndex(q, "/"); i >= 0 {
		base = q[i+1:]
	}
	if base == "" || base == "." || base == ".." {
		return "", fmt.Errorf("unsafe file name refused: %q", p)
	}
	if len(base) >= 2 && base[1] == ':' {
		return "", fmt.Errorf("unsafe file name refused: %q", p)
	}
	return base, nil
}
