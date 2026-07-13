package imagebrowse

// partitions.go — GPT and MBR partition-table parsing over an io.ReaderAt,
// plus filesystem identification from each partition's boot sector.
//
// Hand-rolled (stdlib only): we need exactly one thing — enumerate partitions
// with byte offsets, lengths, and filesystem type — and both on-disk formats
// are small, stable, and fully specified. Fewer dependencies is less to audit
// in a backup agent.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"unicode/utf16"
)

const sectorSize = 512 // PBS machine backups are raw 512n images

// Filesystem identifiers. These strings are displayed by the GUI and switched
// on by OpenFilesystem, so they are stable API, not internal detail.
const (
	FSNTFS      = "ntfs"
	FSFAT12     = "fat12"
	FSFAT16     = "fat16"
	FSFAT32     = "fat32"
	FSExFAT     = "exfat"
	FSReFS      = "refs"
	FSBitLocker = "bitlocker"
	FSNone      = "none"    // no filesystem (e.g. Microsoft Reserved)
	FSUnknown   = "unknown" // has a boot sector we don't recognize
)

// Browsable reports whether we can walk a filesystem's file tree.
func Browsable(fs string) bool {
	switch fs {
	case FSNTFS, FSFAT12, FSFAT16, FSFAT32, FSExFAT:
		return true
	}
	return false
}

// Partition describes one partition of a disk image in absolute byte terms.
type Partition struct {
	Index       int    `json:"index"`        // 1-based, as users expect
	Name        string `json:"name"`         // GPT partition name, if any
	Type        string `json:"type"`         // "Windows data", "EFI system", GUID/ID otherwise
	StartOffset int64  `json:"start_offset"` // bytes from start of disk
	Length      int64  `json:"length"`       // bytes — the ALLOCATED size of the partition
	Filesystem  string `json:"filesystem"`   // one of the FS* constants
	VolumeLabel string `json:"volume_label"` // from the filesystem boot sector, when present
}

// ListPartitions parses the partition table of the disk image behind r.
func ListPartitions(r io.ReaderAt, diskSize int64) ([]Partition, error) {
	lba0 := make([]byte, sectorSize)
	if _, err := r.ReadAt(lba0, 0); err != nil && err != io.EOF {
		return nil, fmt.Errorf("read MBR sector: %w", err)
	}
	if lba0[510] != 0x55 || lba0[511] != 0xAA {
		return nil, fmt.Errorf("no partition table found (missing boot signature) — the image may be a bare volume rather than a whole disk")
	}

	// GPT? Header at LBA 1 with signature "EFI PART". A valid GPT is
	// authoritative; the MBR is then only a protective stub.
	lba1 := make([]byte, sectorSize)
	if _, err := r.ReadAt(lba1, sectorSize); err == nil || err == io.EOF {
		if bytes.Equal(lba1[0:8], []byte("EFI PART")) {
			parts, gerr := parseGPT(r, lba1)
			if gerr != nil {
				// A corrupt GPT behind a valid protective MBR is not a healthy
				// fallback case — report it rather than misparse the stub.
				return nil, fmt.Errorf("GPT present but unreadable: %w", gerr)
			}
			return identifyFilesystems(r, parts), nil
		}
	}

	parts, err := parseMBR(lba0, diskSize)
	if err != nil {
		return nil, err
	}
	return identifyFilesystems(r, parts), nil
}

// ---- GPT --------------------------------------------------------------------

func parseGPT(r io.ReaderAt, hdr []byte) ([]Partition, error) {
	// UEFI spec 5.3.2 header fields (little-endian):
	//   0x48 (8) PartitionEntryLBA
	//   0x50 (4) NumberOfPartitionEntries
	//   0x54 (4) SizeOfPartitionEntry
	entryLBA := binary.LittleEndian.Uint64(hdr[0x48:0x50])
	numEntries := binary.LittleEndian.Uint32(hdr[0x50:0x54])
	entrySize := binary.LittleEndian.Uint32(hdr[0x54:0x58])
	if entrySize < 128 || entrySize > 4096 || numEntries == 0 || numEntries > 1024 {
		return nil, fmt.Errorf("implausible GPT geometry: %d entries x %d bytes", numEntries, entrySize)
	}

	table := make([]byte, int64(numEntries)*int64(entrySize))
	if _, err := r.ReadAt(table, int64(entryLBA)*sectorSize); err != nil && err != io.EOF {
		return nil, fmt.Errorf("read GPT entry array: %w", err)
	}

	var out []Partition
	zeroGUID := make([]byte, 16)
	for i := uint32(0); i < numEntries; i++ {
		e := table[i*entrySize : (i+1)*entrySize]
		if bytes.Equal(e[0:16], zeroGUID) {
			continue // unused slot
		}
		firstLBA := binary.LittleEndian.Uint64(e[32:40])
		lastLBA := binary.LittleEndian.Uint64(e[40:48])
		if lastLBA < firstLBA {
			continue
		}
		nameEnd := 128
		if len(e) < nameEnd {
			nameEnd = len(e)
		}
		out = append(out, Partition{
			Index:       len(out) + 1,
			Name:        decodeUTF16(e[56:nameEnd]),
			Type:        gptTypeName(e[0:16]),
			StartOffset: int64(firstLBA) * sectorSize,
			Length:      int64(lastLBA-firstLBA+1) * sectorSize,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("GPT contains no partitions")
	}
	return out, nil
}

// gptTypeName maps the GUIDs we care about to readable labels. GUIDs are
// mixed-endian on disk: the first three fields are little-endian.
func gptTypeName(g []byte) string {
	guid := fmt.Sprintf("%08X-%04X-%04X-%04X-%012X",
		binary.LittleEndian.Uint32(g[0:4]),
		binary.LittleEndian.Uint16(g[4:6]),
		binary.LittleEndian.Uint16(g[6:8]),
		binary.BigEndian.Uint16(g[8:10]),
		g[10:16])
	switch guid {
	case "EBD0A0A2-B9E5-4433-87C0-68B6B72699C7":
		return "Windows data"
	case "C12A7328-F81F-11D2-BA4B-00A0C93EC93B":
		return "EFI system"
	case "E3C9E316-0B5C-4DB8-817D-F92DF00215AE":
		return "Microsoft reserved"
	case "DE94BBA4-06D1-4D40-A16A-BFD50179D6AC":
		return "Windows recovery"
	case "AF9B60A0-1431-4F62-BC68-3311714A69AD":
		return "Windows LDM data"
	case "0FC63DAF-8483-4772-8E79-3D69D8477DE4":
		return "Linux data"
	case "21686148-6449-6E6F-744E-656564454649":
		return "BIOS boot"
	default:
		return guid
	}
}

func decodeUTF16(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		v := binary.LittleEndian.Uint16(b[i : i+2])
		if v == 0 {
			break
		}
		u = append(u, v)
	}
	return string(utf16.Decode(u))
}

// ---- MBR --------------------------------------------------------------------

func parseMBR(lba0 []byte, diskSize int64) ([]Partition, error) {
	var out []Partition
	for i := 0; i < 4; i++ {
		e := lba0[446+i*16 : 446+(i+1)*16]
		ptype := e[4]
		if ptype == 0x00 {
			continue
		}
		startLBA := binary.LittleEndian.Uint32(e[8:12])
		numSectors := binary.LittleEndian.Uint32(e[12:16])
		if numSectors == 0 {
			continue
		}
		start := int64(startLBA) * sectorSize
		if diskSize > 0 && start >= diskSize {
			continue // garbage entry pointing past the image
		}
		out = append(out, Partition{
			Index:       len(out) + 1,
			Type:        mbrTypeName(ptype),
			StartOffset: start,
			Length:      int64(numSectors) * sectorSize,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("MBR contains no partitions")
	}
	return out, nil
}

func mbrTypeName(t byte) string {
	switch t {
	case 0x07:
		return "NTFS/exFAT data"
	case 0x0B, 0x0C:
		return "FAT32"
	case 0x04, 0x06, 0x0E:
		return "FAT16"
	case 0x01:
		return "FAT12"
	case 0xEE:
		return "GPT protective"
	case 0xEF:
		return "EFI system"
	case 0x83:
		return "Linux data"
	case 0x27:
		return "Windows recovery"
	default:
		return fmt.Sprintf("type 0x%02X", t)
	}
}

// ---- filesystem identification -------------------------------------------------

// identifyFilesystems reads each partition's boot sector and tags the
// filesystem + volume label.
func identifyFilesystems(r io.ReaderAt, parts []Partition) []Partition {
	buf := make([]byte, sectorSize)
	for i := range parts {
		if _, err := r.ReadAt(buf, parts[i].StartOffset); err != nil && err != io.EOF {
			parts[i].Filesystem = FSUnknown
			continue
		}
		fs, label := identifyBootSector(buf)
		parts[i].Filesystem = fs
		parts[i].VolumeLabel = label
	}
	return parts
}

// identifyBootSector classifies a volume from its first 512 bytes.
// Order matters: BitLocker and ReFS occupy the same field NTFS's OEM id uses,
// and exFAT must not be mistaken for FAT (their BPBs overlap, but exFAT
// deliberately zeroes the legacy fields — which is exactly how we tell them
// apart).
func identifyBootSector(b []byte) (string, string) {
	if len(b) < 512 {
		return FSUnknown, ""
	}
	switch string(b[3:11]) {
	case "NTFS    ":
		return FSNTFS, ""
	case "EXFAT   ":
		return FSExFAT, exfatLabelFromBoot(b)
	case "-FVE-FS-":
		// BitLocker replaces NTFS's OEM id with this. Encrypted: nothing to
		// parse without the key.
		return FSBitLocker, ""
	}

	// ReFS: "ReFS\0\0\0\0" at offset 3 AND the "FSRS" structure signature at
	// 0x10 — the pair is what distinguishes it from a stray string.
	if bytes.Equal(b[3:11], []byte{'R', 'e', 'F', 'S', 0, 0, 0, 0}) &&
		bytes.Equal(b[0x10:0x14], []byte("FSRS")) {
		return FSReFS, ""
	}

	// FAT is identified from the BPB, never the OEM string (which is arbitrary).
	if g, err := parseFATGeometry(b); err == nil {
		return g.fsName(), g.labelFromBoot(b)
	}
	return FSUnknown, ""
}
