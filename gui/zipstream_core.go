package main

// zipstream_core.go — STREAMING zip packaging for image-backup selections.
//
// The old flow extracted the whole selection to a staging directory, then
// zipped the directory, then copied the zip — touching every byte three times
// and requiring the selection's size in interim storage TWICE. This file
// replaces that with a single pass: bytes flow PBS -> chunk cache -> NTFS
// parser -> zip entry -> destination, and nothing lands anywhere else.
//
// The zip is a PACKAGING format here, not a compressor: entries use
// zip.Store (no deflate). File data from a disk image is arbitrary binary —
// deflate would burn a core to shave little, and Store keeps throughput at
// I/O speed AND makes the progress/ETA math honest (bytes in == bytes out).
//
// Used by BOTH processes: the GUI's "Package as ZIP" writes straight to the
// user's chosen .zip; the service's portal-delegated image_extract streams
// into its artifact upload. One implementation, mirrored behaviour.

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"imagebrowse"
)

// taskProgress tracks byte-level progress with a smoothed rate and ETA.
// Emit-throttled so the webview isn't flooded (a 5.7 GB file at chunk speed
// would otherwise emit thousands of events).
type taskProgress struct {
	total     int64
	done      int64
	startedAt time.Time
	lastEmit  time.Time
	lastDone  int64
	rateBps   float64 // exponential moving average, bytes/sec
	emit      func(pct float64, msg string, done, total int64, bps float64, etaSec int)
	label     string
}

func newTaskProgress(total int64, label string,
	emit func(pct float64, msg string, done, total int64, bps float64, etaSec int)) *taskProgress {
	return &taskProgress{total: total, startedAt: time.Now(), emit: emit, label: label}
}

// add records n more bytes and emits at most ~5 times/second.
func (t *taskProgress) add(n int64) {
	t.done += n
	now := time.Now()
	if now.Sub(t.lastEmit) < 200*time.Millisecond && t.done < t.total {
		return
	}
	dt := now.Sub(t.lastEmit).Seconds()
	if t.lastEmit.IsZero() {
		dt = now.Sub(t.startedAt).Seconds()
	}
	if dt > 0 {
		inst := float64(t.done-t.lastDone) / dt
		if t.rateBps == 0 {
			t.rateBps = inst
		} else {
			// EMA smooths chunk-boundary bursts into a readable rate.
			t.rateBps = 0.7*t.rateBps + 0.3*inst
		}
	}
	t.lastEmit, t.lastDone = now, t.done
	pct := 0.0
	if t.total > 0 {
		pct = float64(t.done) / float64(t.total) * 100
	}
	eta := -1
	if t.rateBps > 1 && t.total > t.done {
		eta = int(float64(t.total-t.done) / t.rateBps)
	}
	t.emit(pct, t.label, t.done, t.total, t.rateBps, eta)
}

// countingWriter feeds a taskProgress as bytes pass through.
type countingWriter struct {
	w io.Writer
	t *taskProgress
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.t.add(int64(n))
	}
	return n, err
}

// planSelection expands a selection and pre-computes its total byte size so
// progress can be byte-accurate from the first emit. Returns the file list
// and total. The Stat calls hit the already-cached file table — no I/O.
func planSelection(fs imagebrowse.Filesystem, includePaths []string) ([]string, int64, error) {
	files, err := expandImageSelection(fs, includePaths)
	if err != nil {
		return nil, 0, err
	}
	if len(files) == 0 {
		return nil, 0, ibFail(errors.New("[NB-3420] the selection contains no files"))
	}
	var total int64
	for _, f := range files {
		if st, serr := fs.Stat(f); serr == nil {
			total += int64(st.Size)
		}
	}
	return files, total, nil
}

// streamImageZip writes the selection as a Store-method zip DIRECTLY to w in
// one pass. Entry names preserve the in-image relative structure; entry
// mtimes carry the source file's mtime (that much fidelity a zip CAN hold —
// NTFS permissions and ADS cannot travel in a zip, which is exactly why the
// UI greys those options in zip mode).
func streamImageZip(fs imagebrowse.Filesystem, files []string, totalBytes int64, w io.Writer,
	emit func(pct float64, msg string, done, total int64, bps float64, etaSec int),
	cancelled func() bool) (int, int64, error) {

	prog := newTaskProgress(totalBytes, "", emit)
	zw := zip.NewWriter(w)
	count := int(0)

	for _, f := range files {
		if cancelled != nil && cancelled() {
			_ = zw.Close()
			return count, prog.done, errImageRestoreCancelled
		}
		name := strings.TrimPrefix(path.Clean("/"+f), "/")
		if name == "" {
			continue
		}
		hdr := &zip.FileHeader{
			Name:   name,
			Method: zip.Store, // packaging, not compression — see file comment
		}
		if st, serr := fs.Stat(f); serr == nil && st.ModTime > 0 {
			hdr.Modified = time.Unix(st.ModTime, 0)
		}
		prog.label = path.Base(name)
		entry, err := zw.CreateHeader(hdr)
		if err != nil {
			_ = zw.Close()
			return count, prog.done, fmt.Errorf("[NB-3430] zip entry %s: %w", name, err)
		}
		if _, err := fs.ExtractFile(f, &countingWriter{w: entry, t: prog}); err != nil {
			_ = zw.Close()
			return count, prog.done, fmt.Errorf("[NB-3421] extract %s: %w", f, err)
		}
		count++
	}
	if err := zw.Close(); err != nil {
		return count, prog.done, fmt.Errorf("[NB-3430] finalize zip: %w", err)
	}
	return count, prog.done, nil
}
