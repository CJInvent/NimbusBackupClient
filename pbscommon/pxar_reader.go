package pbscommon

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PXARReader reads and extracts PXAR archives.
// Supports nested directories, selective extraction, and content listing.
// Symlinks, ACLs, xattrs and devices are still skipped (read past) for now.
type PXARReader struct {
	data   []byte
	offset int64
}

// PXARHeader represents a generic PXAR entry header.
type PXARHeader struct {
	Type uint64
	Size uint64
}

// PXARTreeEntry describes a file or directory found in a PXAR archive.
// Path uses forward slashes (archive style) and is relative to the archive root.
type PXARTreeEntry struct {
	Path    string
	IsDir   bool
	Size    uint64
	Mode    uint32
	ModTime int64
}

// PXARExtractedFile represents an extracted file (or directory) with metadata.
type PXARExtractedFile struct {
	Path       string
	Size       uint64
	Mode       os.FileMode
	ModTime    int64
	IsDir      bool
	Data       []byte
	Skipped    bool
	SkipReason string
}

// NewPXARReader creates a new PXAR reader from raw data.
func NewPXARReader(data []byte) *PXARReader {
	return &PXARReader{data: data, offset: 0}
}

func (pr *PXARReader) readHeader() (*PXARHeader, error) {
	if pr.offset+16 > int64(len(pr.data)) {
		return nil, io.EOF
	}
	h := &PXARHeader{}
	buf := bytes.NewReader(pr.data[pr.offset : pr.offset+16])
	if err := binary.Read(buf, binary.LittleEndian, &h.Type); err != nil {
		return nil, err
	}
	if err := binary.Read(buf, binary.LittleEndian, &h.Size); err != nil {
		return nil, err
	}
	return h, nil
}

func (pr *PXARReader) skip(n int64) { pr.offset += n }

func (pr *PXARReader) read(n int64) ([]byte, error) {
	if pr.offset+n > int64(len(pr.data)) {
		return nil, io.EOF
	}
	data := pr.data[pr.offset : pr.offset+n]
	pr.offset += n
	return data, nil
}

// reset rewinds the reader to the beginning so the same archive can be walked again.
func (pr *PXARReader) reset() { pr.offset = 0 }

// walkCallback is invoked for each file or directory entry encountered.
// payload is non-nil only for files (it carries the raw file content).
type walkCallback func(entry PXARTreeEntry, payload []byte) error

// walk iterates the entire PXAR archive, invoking cb for each entry.
// Correctly tracks the directory stack via PXAR_GOODBYE markers, so nested
// directories and empty directories are handled properly.
func (pr *PXARReader) walk(cb walkCallback) error {
	pr.reset()
	var pathStack []string
	currentPath := ""
	pendingName := ""
	var pendingFileMode uint64
	var pendingFileMtime uint64
	hasPendingFile := false
	rootSeen := false

	for {
		header, err := pr.readHeader()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read header at offset %d: %w", pr.offset, err)
		}
		if header.Size < 16 {
			return fmt.Errorf("invalid header size %d at offset %d", header.Size, pr.offset)
		}
		contentSize := int64(header.Size) - 16

		switch header.Type {
		case PXAR_FILENAME:
			pr.skip(16)
			data, err := pr.read(contentSize)
			if err != nil {
				return fmt.Errorf("read filename: %w", err)
			}
			pendingName = string(bytes.TrimRight(data, "\x00"))

		case PXAR_ENTRY, PXAR_ENTRY_V1:
			pr.skip(16)
			data, err := pr.read(contentSize)
			if err != nil {
				return fmt.Errorf("read entry: %w", err)
			}
			// Parse PXARFileEntry payload by byte offset. We avoid binary.Read
			// on struct pointers because PXARFileEntry/MTime have unexported
			// fields that reflect.Value cannot Set, which silently zeroes mtime.
			// Layout: mode(u64) | flags(u64) | uid(u32) | gid(u32) | mtime.secs(u64) | mtime.nanos(u32) | mtime.padding(u32) = 40 bytes
			var mode uint64
			var mtimeSecs uint64
			if len(data) >= 32 {
				mode = binary.LittleEndian.Uint64(data[0:8])
				mtimeSecs = binary.LittleEndian.Uint64(data[24:32])
			}

			if (mode & IFDIR) != 0 {
				// Directory entry. The first ENTRY in the archive is the root
				// (no preceding FILENAME) and is not emitted as its own entry.
				if !rootSeen {
					rootSeen = true
				} else {
					subPath := joinArchivePath(currentPath, pendingName)
					if err := cb(PXARTreeEntry{
						Path:    subPath,
						IsDir:   true,
						Mode:    uint32(mode),
						ModTime: int64(mtimeSecs),
					}, nil); err != nil {
						return err
					}
					pathStack = append(pathStack, currentPath)
					currentPath = subPath
				}
				pendingName = ""
				hasPendingFile = false
			} else {
				// Regular file: defer emission until the matching PAYLOAD arrives.
				pendingFileMode = mode
				pendingFileMtime = mtimeSecs
				hasPendingFile = true
			}

		case PXAR_PAYLOAD:
			pr.skip(16)
			data, err := pr.read(contentSize)
			if err != nil {
				return fmt.Errorf("read payload: %w", err)
			}
			if hasPendingFile {
				path := joinArchivePath(currentPath, pendingName)
				if err := cb(PXARTreeEntry{
					Path:    path,
					IsDir:   false,
					Size:    uint64(len(data)),
					Mode:    uint32(pendingFileMode),
					ModTime: int64(pendingFileMtime),
				}, data); err != nil {
					return err
				}
				hasPendingFile = false
				pendingName = ""
			}

		case PXAR_GOODBYE:
			pr.skip(int64(header.Size))
			if len(pathStack) > 0 {
				currentPath = pathStack[len(pathStack)-1]
				pathStack = pathStack[:len(pathStack)-1]
			} else {
				currentPath = ""
			}

		default:
			// Symlinks, devices, xattrs, ACLs, FCAPs, hardlinks, quota etc.
			// These are skipped for now — they belong to file metadata that
			// the upcoming NTFS sidecar work will handle properly.
			pr.skip(int64(header.Size))
		}
	}
	return nil
}

// joinArchivePath joins archive paths with forward slashes, never producing
// a leading or duplicate slash.
func joinArchivePath(parent, child string) string {
	if parent == "" {
		return child
	}
	if child == "" {
		return parent
	}
	return parent + "/" + child
}

// ListEntries returns all files and directories in the archive without extracting
// any payload. Useful for displaying a navigable tree before restore.
func (pr *PXARReader) ListEntries() ([]PXARTreeEntry, error) {
	entries := make([]PXARTreeEntry, 0, 256)
	err := pr.walk(func(e PXARTreeEntry, _ []byte) error {
		entries = append(entries, e)
		return nil
	})
	return entries, err
}

// ExtractAll extracts the entire PXAR archive to destDir.
func (pr *PXARReader) ExtractAll(destDir string) ([]PXARExtractedFile, error) {
	return pr.ExtractFiltered(destDir, nil, false)
}

// ExtractFiltered extracts entries whose archive path matches one of includePaths.
// An empty includePaths means "extract everything". Selecting a directory implies
// all its descendants. Existing files are skipped unless overwrite is true.
//
// includePaths may use either forward or backward slashes; they are normalized.
func (pr *PXARReader) ExtractFiltered(destDir string, includePaths []string, overwrite bool) ([]PXARExtractedFile, error) {
	includes := normalizeIncludes(includePaths)
	extracted := make([]PXARExtractedFile, 0, 64)

	err := pr.walk(func(e PXARTreeEntry, payload []byte) error {
		if !pathMatches(e.Path, includes) {
			return nil
		}

		rel := filepath.FromSlash(e.Path)
		fullPath := filepath.Join(destDir, rel)

		if e.IsDir {
			if err := os.MkdirAll(fullPath, 0755); err != nil {
				extracted = append(extracted, PXARExtractedFile{
					Path: fullPath, IsDir: true,
					Skipped: true, SkipReason: fmt.Sprintf("mkdir: %v", err),
				})
				return nil
			}
			extracted = append(extracted, PXARExtractedFile{
				Path: fullPath, IsDir: true,
				Mode: os.FileMode(e.Mode & 0777), ModTime: e.ModTime,
			})
			return nil
		}

		// File: ensure parent dir exists (a parent might not have been
		// emitted yet if the user selected a deep path directly).
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			extracted = append(extracted, PXARExtractedFile{
				Path: fullPath, Size: e.Size,
				Skipped: true, SkipReason: fmt.Sprintf("mkdir parent: %v", err),
			})
			return nil
		}

		if !overwrite {
			if _, err := os.Stat(fullPath); err == nil {
				extracted = append(extracted, PXARExtractedFile{
					Path: fullPath, Size: e.Size,
					Skipped: true, SkipReason: "already exists",
				})
				return nil
			}
		}

		if err := os.WriteFile(fullPath, payload, os.FileMode(e.Mode&0777)); err != nil {
			extracted = append(extracted, PXARExtractedFile{
				Path: fullPath, Size: e.Size,
				Skipped: true, SkipReason: fmt.Sprintf("write: %v", err),
			})
			return nil
		}
		if e.ModTime > 0 {
			t := time.Unix(e.ModTime, 0)
			_ = os.Chtimes(fullPath, t, t)
		}
		extracted = append(extracted, PXARExtractedFile{
			Path: fullPath, Size: e.Size,
			Mode: os.FileMode(e.Mode & 0777), ModTime: e.ModTime,
		})
		return nil
	})
	return extracted, err
}

func normalizeIncludes(in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.ReplaceAll(p, "\\", "/")
		p = strings.Trim(p, "/")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// pathMatches returns true when path should be extracted given the include list.
// A path matches when it is one of the includes, a descendant of an include, or
// an ancestor of an include (so parent directories are created as needed).
func pathMatches(path string, includes []string) bool {
	if len(includes) == 0 {
		return true
	}
	for _, inc := range includes {
		if path == inc {
			return true
		}
		if strings.HasPrefix(path, inc+"/") {
			return true
		}
		if strings.HasPrefix(inc, path+"/") {
			return true
		}
	}
	return false
}
