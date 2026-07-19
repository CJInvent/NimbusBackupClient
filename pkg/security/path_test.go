package security

import (
	"path/filepath"
	"strings"
	"testing"
)

// The vectors below are not hypothetical shapes: they are what a crafted or
// corrupted backup image can carry in an NTFS/FAT filename field, which the
// image-restore path used to hand straight to filepath.Join.
func TestIsUnsafeRelPathRefusesTraversal(t *testing.T) {
	unsafe := []string{
		"..",
		"../x",
		"a/../../x",
		"a/b/../../../etc/passwd",
		`..\..\..\Windows\System32\evil.dll`, // the realistic crafted-name case
		`a\..\..\x`,
		"/etc/passwd",      // unix-absolute
		`\Windows\evil`,    // root-relative on Windows
		`\\server\share\x`, // UNC
		"C:/Windows/evil",
		`C:\Windows\evil`,
		"C:evil", // drive-RELATIVE: resolves against C:'s working directory
		"",
		"a\x00b",
	}
	for _, p := range unsafe {
		if !IsUnsafeRelPath(p) {
			t.Errorf("IsUnsafeRelPath(%q) = false, want true — this path can escape a restore root", p)
		}
	}
}

func TestIsUnsafeRelPathAllowsOrdinaryNames(t *testing.T) {
	safe := []string{
		"file.txt",
		"Users/alice/Documents/report.docx",
		"a/b/c",
		"notes..txt", // ".." inside a name is not a traversal component
		"..hidden",   // leading dots are legal filename characters
		"weird...name",
		"space in name.txt",
		"héllo/wörld.txt",
		"$WINDOWS.~BT/x", // legitimate $-prefixed user directory
		".",              // resolves to the root itself, not outside it
	}
	for _, p := range safe {
		if IsUnsafeRelPath(p) {
			t.Errorf("IsUnsafeRelPath(%q) = true, want false — refusing legitimate files loses restorable data", p)
		}
	}
}

// The bug this guard exists for: filepath.Join RESOLVES "..", it does not
// reject it, so the pre-fix code silently wrote outside the destination.
func TestSafeJoinBlocksTheEscapeThatJoinAllows(t *testing.T) {
	root := filepath.Join("restore", "dest")
	hostile := "../../../etc/passwd"

	// Demonstrate the unsafe behavior the product used to have.
	escaped := filepath.Join(root, filepath.FromSlash(hostile))
	if strings.HasPrefix(escaped, filepath.Clean(root)+string(filepath.Separator)) {
		t.Fatalf("test premise wrong: %q did not escape %q", escaped, root)
	}
	t.Logf("plain filepath.Join escapes to %q", escaped)

	// SafeJoin refuses it.
	if got, err := SafeJoin(root, hostile); err == nil {
		t.Errorf("SafeJoin allowed an escape to %q", got)
	}
}

func TestSafeJoinContainsResults(t *testing.T) {
	root := filepath.Join("a", "restore-root")
	cleanRoot := filepath.Clean(root)

	for _, rel := range []string{"file.txt", "sub/dir/file.txt", "notes..txt", "."} {
		got, err := SafeJoin(root, rel)
		if err != nil {
			t.Errorf("SafeJoin(%q, %q) failed: %v", root, rel, err)
			continue
		}
		if got != cleanRoot && !strings.HasPrefix(got, cleanRoot+string(filepath.Separator)) {
			t.Errorf("SafeJoin(%q, %q) = %q, which is outside the root", root, rel, got)
		}
	}

	for _, rel := range []string{"../escape", `..\escape`, "/abs", "C:/abs", "", "a/../.."} {
		if got, err := SafeJoin(root, rel); err == nil {
			t.Errorf("SafeJoin(%q, %q) = %q, want refusal", root, rel, got)
		}
	}

	if _, err := SafeJoin("", "file.txt"); err == nil {
		t.Error("SafeJoin with an empty root should fail")
	}
}

// Flattening a selection does not sanitize a hostile name: Base("..") is "..",
// and joining that to a destination yields the destination's PARENT.
func TestSafeBaseName(t *testing.T) {
	ok := map[string]string{
		"file.txt":                  "file.txt",
		"a/b/report.docx":           "report.docx",
		`a\b\report.docx`:           "report.docx",
		"..hidden":                  "..hidden",
		"notes..txt":                "notes..txt",
		`..\..\..\Windows\evil.dll`: "evil.dll",
	}
	for in, want := range ok {
		got, err := SafeBaseName(in)
		if err != nil {
			t.Errorf("SafeBaseName(%q) failed: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("SafeBaseName(%q) = %q, want %q", in, got, want)
		}
	}

	for _, bad := range []string{"", "..", ".", "a/..", `a\..`, "a/", "C:", "x\x00y"} {
		if got, err := SafeBaseName(bad); err == nil {
			t.Errorf("SafeBaseName(%q) = %q, want refusal", bad, got)
		}
	}
}
