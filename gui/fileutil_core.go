package main

// fileutil_core.go — shared file/space helpers used by BOTH processes.
// Moved out of download.go (which is GUI-only, !service) so the Windows
// service can package image-browse extractions for the control server —
// the portal's delegated browse (see controlplane_glue.go) runs here.

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SpaceCheck is the result of a free-space evaluation for a pending write of
// neededBytes onto the drive holding path.
type SpaceCheck struct {
	Path          string  `json:"path"`
	FreeBytes     uint64  `json:"free_bytes"`
	TotalBytes    uint64  `json:"total_bytes"`
	NeededBytes   uint64  `json:"needed_bytes"`
	Fits          bool    `json:"fits"`    // needed <= free
	Warn90        bool    `json:"warn_90"` // fits, but usage after >= 90%
	UsageAfterPct float64 `json:"usage_after_pct"`
}

func evaluateSpace(path string, needed uint64) (SpaceCheck, error) {
	free, total, err := driveSpace(path)
	if err != nil {
		return SpaceCheck{}, err
	}
	sc := SpaceCheck{Path: path, FreeBytes: free, TotalBytes: total, NeededBytes: needed}
	sc.Fits = needed <= free
	if total > 0 {
		// used_after = total - free + needed. Guard the arithmetic: all
		// uint64, and needed > free is already the !Fits case (no subtraction
		// underflow possible in the used computation: total >= free always).
		usedAfter := (total - free) + needed
		sc.UsageAfterPct = float64(usedAfter) / float64(total) * 100.0
		sc.Warn90 = sc.Fits && float64(usedAfter) >= 0.90*float64(total)
	}
	return sc, nil
}

// zipDirectory writes every file under root into a zip at destZip, with
// paths relative to root (forward slashes, per the zip spec).
func zipDirectory(root, destZip string) error {
	out, err := os.Create(destZip)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			// Explicit dir entries keep empty folders.
			_, err := zw.Create(rel + "/")
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = rel
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(w, f)
		return err
	})
	if cerr := zw.Close(); walkErr == nil {
		walkErr = cerr
	}
	if cerr := out.Close(); walkErr == nil {
		walkErr = cerr
	}
	if walkErr != nil {
		_ = os.Remove(destZip) // no half-written zips
	}
	return walkErr
}

// findSingleFile returns the path of the only regular file under root.
func findSingleFile(root string) (string, error) {
	var found string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if found != "" {
			return fmt.Errorf("expected a single file, found several")
		}
		found = path
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("extraction produced no file")
	}
	return found, nil
}

func copyFileTo(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// formatBytesGo mirrors the frontend's formatBytes for error strings.
func formatBytesGo(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	if exp >= len(units) {
		exp = len(units) - 1
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(b)/float64(div)), ".0") + " " + units[exp]
}
