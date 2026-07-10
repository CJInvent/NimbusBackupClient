package imagebrowse

// partitions.go — GPT and MBR partition-table parsing over an io.ReaderAt.
//
// Hand-rolled (~stdlib only) rather than pulling a disk library: we need
// exactly one operation — enumerate partitions with byte offsets/lengths —
// and both on-disk formats are small, stable, and fully specified. Fewer
// dependencies means less to verify in CI and a smaller supply-chain surface
// for a backup agent.
//
// Strategy: read LBA 0 (protective/legacy MBR) and LBA 1 (GPT header). If a
// valid GPT header is present, parse the GPT entry array (authoritative —
// the MBR is then just the protective stub). Otherwise fall back to the four
// primary MBR entries. Extended/logical MBR partitions are out of scope for
// v1 (vanishingly rare on Windows machine images, which is what our volume
// backups contain).

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"unicode/utf16"
)

const sectorSize = 512 // fixed for our images: PBS machine backups are raw 512n images

// Partition describes one partition of a disk image in absolute byte terms.
type Partition struct {
	Index       int    `json:"index"`        // 1-based, as users expect
	Name        string `json:"name"`         // GPT partition name (UTF-16 label), if any
	Type        string `json:"type"`         // human hint: "NTFS/exFAT data", "EFI system", GUID/ID string otherwise
	StartOffset int64  `json:"start_offset"` // bytes from start of disk
	Length      int64  `json:"length"`       // bytes
	Filesystem  string `json:"filesystem"`   // sniffed: "ntfs", "fat", "bitlocker", "unknown"
}

// ListPartitions parses the partition table of the disk image behind r.
func ListPartitions(r io.ReaderAt, diskSize int64) ([]Partition, error) {
	lba0 := make([]byte, sectorSize)
	if _, err := r.ReadAt(lba0, 0); err != nil && err != io.EOF {
		return nil, fmt.Errorf("read MBR sector: %w", err)
	}
	if lba0[510] != 0x55 || lba0[511] != 0xAA {
		return nil, fmt.Errorf("no partition table: missing MBR boot signature")
	}

	// GPT? Header lives at LBA 1 with signature "EFI PART".
	lba1 := make([]byte, sectorSize)
	if _, err := r.ReadAt(lba1, sectorSize); err == nil || err == io.EOF {
		if bytes.Equal(lba1[0:8], []byte("EFI PART")) {
			parts, gerr := parseGPT(r, lba1)
			if gerr == nil {
				return sniffFilesystems(r, parts), nil
			}
			// A corrupt GPT with a valid protective MBR is not a healthy
			// fallback case — surface the GPT error rather than misparse.
			return nil, fmt.Errorf("GPT present but unreadable: %w", gerr)
		}
	}

	parts, err := parseMBR(lba0, diskSize)
	if err != nil {
		return nil, err
	}
	return sniffFilesystems(r, parts), nil
}

// ---- GPT --------------------------------------------------------------------

func parseGPT(r io.ReaderAt, hdr []byte) ([]Partition, error) {
	// Header layout (little-endian) — offsets per UEFI spec 5.3.2:
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
		typeGUID := e[0:16]
		if bytes.Equal(typeGUID, zeroGUID) {
			continue // unused slot
		}
		firstLBA := binary.LittleEndian.Uint64(e[32:40])
		lastLBA := binary.LittleEndian.Uint64(e[40:48])
		if lastLBA < firstLBA {
			continue
		}
		// Partition name: UTF-16LE, up to 36 code units at offset 56.
		name := decodeUTF16(e[56:min(len(e), 56+72)])
		out = append(out, Partition{
			Index:       len(out) + 1,
			Name:        name,
			Type:        gptTypeName(typeGUID),
			StartOffset: int64(firstLBA) * sectorSize,
			Length:      int64(lastLBA-firstLBA+1) * sectorSize,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("GPT contains no partitions")
	}
	return out, nil
}

// gptTypeName maps the handful of GUIDs we care about to a readable label.
// GUID mixed-endian encoding: first three fields little-endian on disk.
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
	case "0FC63DAF-8483-4772-8E79-3D69D8477DE4":
		return "Linux data"
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
		length := int64(numSectors) * sectorSize
		if diskSize > 0 && start >= diskSize {
			continue // garbage entry pointing past the image
		}
		out = append(out, Partition{
			Index:       len(out) + 1,
			Type:        mbrTypeName(ptype),
			StartOffset: start,
			Length:      length,
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
	case 0x0E, 0x06:
		return "FAT16"
	case 0xEE:
		return "GPT protective"
	case 0x83:
		return "Linux data"
	case 0x27:
		return "Windows recovery"
	default:
		return fmt.Sprintf("type 0x%02X", t)
	}
}

// ---- filesystem sniffing ------------------------------------------------------

// sniffFilesystems reads each partition's boot sector and tags the filesystem.
// BitLocker volumes advertise the "-FVE-FS-" OEM ID in place of "NTFS    " —
// detecting that lets the UI say "encrypted, cannot browse" instead of a
// confusing parse failure.
func sniffFilesystems(r io.ReaderAt, parts []Partition) []Partition {
	buf := make([]byte, sectorSize)
	for i := range parts {
		parts[i].Filesystem = "unknown"
		if _, err := r.ReadAt(buf, parts[i].StartOffset); err != nil && err != io.EOF {
			continue
		}
		oem := string(buf[3:11])
		switch {
		case oem == "NTFS    ":
			parts[i].Filesystem = "ntfs"
		case oem == "-FVE-FS-":
			parts[i].Filesystem = "bitlocker"
		case oem == "EXFAT   ":
			parts[i].Filesystem = "exfat"
		case bytes.Equal(buf[82:87], []byte("FAT32")):
			parts[i].Filesystem = "fat"
		case bytes.Equal(buf[54:59], []byte("FAT16")) || bytes.Equal(buf[54:59], []byte("FAT12")):
			parts[i].Filesystem = "fat"
		}
	}
	return parts
}
