package imagebrowse

// ntfs_security.go — NTFS permissions (security descriptors) read straight
// from the volume, so image restores can carry ACLs across with NO sidecar:
// the disk image already contains everything.
//
// How NTFS stores permissions since NT 5: files do not carry their security
// descriptor inline. Instead $STANDARD_INFORMATION holds a 4-byte SecurityId,
// which indexes into the $Secure metafile (MFT record 9). $Secure's $SDS
// stream is the actual store: a sequence of entries
//
//	{ Hash u32 | Id u32 | Offset u64 | Length u32 } + self-relative SD
//
// 16-byte aligned, where Length includes the 20-byte header. The stream is
// written in 256 KiB blocks and each block is DUPLICATED into the following
// block for resilience; mirror copies keep the ORIGINAL Offset in their
// header, which is exactly how we skip them: accept an entry only when its
// header Offset matches its actual position.

import (
	"encoding/binary"
	"fmt"
	"io"

	ntfs "www.velocidex.com/golang/go-ntfs/parser"
)

const (
	ntfsAttrStandardInfo = 16        // $STANDARD_INFORMATION attribute type
	sdsHeaderLen         = 20        // hash+id+offset+length
	sdsBlock             = 256 << 10 // duplication block size
	sdsMaxStream         = 256 << 20 // refuse absurd $SDS sizes
	sdsMaxEntry          = 512 << 10 // one SD larger than this is corrupt
)

// loadSecurityIndex builds the SecurityId -> self-relative-SD map from $SDS,
// once per opened filesystem. Only invoked when a caller actually asks for
// permissions, so plain browsing never pays for it.
func (f *ntfsFS) loadSecurityIndex() error {
	f.sdsOnce.Do(func() {
		f.sds, f.sdsErr = f.scanSDS()
	})
	return f.sdsErr
}

func (f *ntfsFS) scanSDS() (map[uint32][]byte, error) {
	secure, err := f.ctx.GetMFT(9) // $Secure is MFT record 9 by definition
	if err != nil {
		return nil, fmt.Errorf("read $Secure record: %w", err)
	}
	// Find the $SDS stream size, then open it.
	var size int64 = -1
	for _, attr := range secure.EnumerateAttributes(f.ctx) {
		if attr.Type().Value == ntfsAttrData && attr.Name() == "$SDS" {
			size = int64(ntfsAttrSize(attr))
			break
		}
	}
	if size <= 0 {
		return nil, fmt.Errorf("$Secure has no $SDS stream (very old volume?)")
	}
	if size > sdsMaxStream {
		return nil, fmt.Errorf("$SDS is %d bytes — refusing to load", size)
	}
	reader, err := ntfs.OpenStream(f.ctx, secure, ntfsAttrData, 65535, "$SDS")
	if err != nil {
		return nil, fmt.Errorf("open $SDS: %w", err)
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(io.NewSectionReader(reader, 0, size), raw); err != nil {
		return nil, fmt.Errorf("read $SDS: %w", err)
	}

	out := make(map[uint32][]byte)
	pos := int64(0)
	for pos+sdsHeaderLen <= size {
		id := binary.LittleEndian.Uint32(raw[pos+4 : pos+8])
		off := binary.LittleEndian.Uint64(raw[pos+8 : pos+16])
		length := binary.LittleEndian.Uint32(raw[pos+16 : pos+20])

		valid := id != 0 &&
			length > sdsHeaderLen && int64(length) <= sdsMaxEntry &&
			int64(off) == pos && // mirror copies carry the ORIGINAL offset -> rejected here
			pos+int64(length) <= size

		if !valid {
			// Either padding, a mirror block, or garbage: jump to the next
			// 256 KiB block boundary and resume scanning there.
			pos = (pos/sdsBlock + 1) * sdsBlock
			continue
		}
		sd := make([]byte, length-sdsHeaderLen)
		copy(sd, raw[pos+sdsHeaderLen:pos+int64(length)])
		if _, dup := out[id]; !dup {
			out[id] = sd
		}
		// Entries are 16-byte aligned.
		pos += (int64(length) + 15) &^ 15
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no security descriptors found in $SDS")
	}
	return out, nil
}

// securityID reads the SecurityId from a file's $STANDARD_INFORMATION.
// SI is always resident; the field sits at offset 0x34 of its content (only
// present in the 72-byte NT5+ layout — a 48-byte SI means a pre-SecurityId
// volume, reported as absent).
func securityID(ctx *ntfs.NTFSContext, e *ntfs.MFT_ENTRY) (uint32, bool) {
	for _, attr := range e.EnumerateAttributes(ctx) {
		if attr.Type().Value != ntfsAttrStandardInfo || !attr.IsResident() {
			continue
		}
		content := make([]byte, attr.Content_size())
		if len(content) < 0x38 {
			return 0, false
		}
		n, err := attr.Reader.ReadAt(content, attr.Offset+int64(attr.Content_offset()))
		if err != nil && err != io.EOF {
			return 0, false
		}
		if n < 0x38 {
			return 0, false
		}
		return binary.LittleEndian.Uint32(content[0x34:0x38]), true
	}
	return 0, false
}

const ntfsAttrSecurityDescriptor = 80 // legacy per-file $SECURITY_DESCRIPTOR

// SecurityDescriptor implements SecurityReader: the self-relative security
// descriptor bytes for p, exactly as the OS stored them. Two storage models
// exist and we support both:
//   - NT5+ (every Windows-formatted volume): $STANDARD_INFORMATION carries a
//     SecurityId indexing the shared $Secure/$SDS store.
//   - Legacy (and ntfs-3g-written volumes): a per-file $SECURITY_DESCRIPTOR
//     attribute (type 0x50) holds the SD inline.
func (f *ntfsFS) SecurityDescriptor(p string) ([]byte, error) {
	e, err := f.open(p)
	if err != nil {
		return nil, err
	}
	// Modern path first: SecurityId -> $SDS.
	if id, ok := securityID(f.ctx, e); ok && id != 0 {
		if err := f.loadSecurityIndex(); err == nil {
			if sd, ok := f.sds[id]; ok {
				return sd, nil
			}
		}
	}
	// Legacy fallback: inline $SECURITY_DESCRIPTOR attribute.
	for _, attr := range e.EnumerateAttributes(f.ctx) {
		if attr.Type().Value != ntfsAttrSecurityDescriptor || !attr.IsResident() {
			continue
		}
		size := int64(attr.Content_size())
		if size < 20 || size > sdsMaxEntry {
			continue
		}
		sd := make([]byte, size)
		n, rerr := attr.Reader.ReadAt(sd, attr.Offset+int64(attr.Content_offset()))
		if rerr != nil && rerr != io.EOF {
			continue
		}
		if int64(n) == size {
			return sd, nil
		}
	}
	return nil, fmt.Errorf("%s: no security descriptor stored (neither SecurityId nor inline)", p)
}
