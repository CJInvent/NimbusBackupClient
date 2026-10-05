package main

import (
	"bytes"
	"controlplane"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Storage identity from the bytes Windows returns for an OPEN device
// (IOCTL_STORAGE_QUERY_PROPERTY), and what that identity is worth.
//
// Untagged so the Linux gate tests it; storage_handle_windows.go does the
// DeviceIoControl calls and hands the bytes here.
//
// STRENGTH (docs/V4-BETA-FIXES.md §5). A device's hardware identifiers say how
// much its identity is worth. STRONG means the device reports something that
// belongs to the medium: an NAA or EUI-64 designator, or a serial. WEAK means
// the only identifier it reports names the slot it is attached to (a
// designator such as "drive-scsi1"), so the identity changes when the disk
// moves to another slot and a clone in the same slot keeps it. A device that
// reports nothing at all has no hardware identifier either.
//
// Neither a weak nor a missing identifier blocks approval. Such a disk is
// identified by its disk GUID (or MBR signature), size and partition layout,
// the same evidence every approval already checks
// (controlplane.ResolveStorageBinding). A disk with no hardware identifier at
// all is given a stable ID derived from that GUID (diskLayoutIdentity) and
// reported weak; that is not a new kind of identity, only a name for it.
//
// Everything that yields a hardware identifier is "strong" unless it is a
// slot name:
//   - an NAA (type 3) or EUI-64 (type 2) designator, or any designator that is
//     neither vendor-specific nor T10;
//   - a vendor-specific or T10 designator whose text is not a slot name;
//   - a binary vendor-specific designator (there is no text to be a slot name).
// Only slot shapes seen on a real machine are listed (rule 25); others are
// added when captured.

// STORAGE_IDENTIFIER_TYPE and STORAGE_IDENTIFIER_CODE_SET values (ntddstor.h).
const (
	storageIDTypeVendorSpecific = 0
	storageIDTypeVendorID       = 1 // T10: 8-byte vendor, then vendor-specific text
	storageIDCodeSetASCII       = 2
	storageIDCodeSetUTF8        = 3
)

var storageSlotName = regexp.MustCompile(`^drive-(scsi|sata|ide|virtio)[0-9]+$`)

type storageIdentity struct {
	ID       string
	Strength string
}

// storageDescriptorIdentity reads a STORAGE_DEVICE_ID_DESCRIPTOR (page-0x83
// designators). The ID hashes every device-associated designator exactly as
// before F-39, so existing approvals keep their IDs; the strength is read
// from the same designators, so an equal ID always means an equal strength.
func storageDescriptorIdentity(data []byte) (storageIdentity, error) {
	var zero storageIdentity
	if len(data) < 12 {
		return zero, errors.New("short device identifier header")
	}
	size := int(binary.LittleEndian.Uint32(data[4:]))
	count := int(binary.LittleEndian.Uint32(data[8:]))
	if size < 12 || size > len(data) || count < 1 || count > 128 {
		return zero, errors.New("invalid device identifier header")
	}
	data = data[:size]
	pos := 12
	var ids []string
	weak := true
	for i := 0; i < count; i++ {
		if pos > len(data)-16 {
			return zero, errors.New("truncated device identifier")
		}
		d := data[pos:]
		codeSet := binary.LittleEndian.Uint32(d)
		kind := binary.LittleEndian.Uint32(d[4:])
		length := int(binary.LittleEndian.Uint16(d[8:]))
		next := int(binary.LittleEndian.Uint16(d[10:]))
		association := binary.LittleEndian.Uint32(d[12:])
		if length == 0 || length > len(d)-16 {
			return zero, errors.New("invalid device identifier size")
		}
		if association == 0 {
			payload := d[16 : 16+length]
			ids = append(ids, fmt.Sprintf("%d:%d:%x", codeSet, kind, payload))
			if !designatorIsSlotName(codeSet, kind, payload) {
				weak = false
			}
		}
		if i+1 < count {
			if next < 16+length || next > len(d)-16 {
				return zero, errors.New("invalid next device identifier")
			}
			pos += next
		}
	}
	if len(ids) == 0 {
		return zero, errors.New("no device-associated identifiers")
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte("vpd-v1\x00" + strings.Join(ids, "\x00")))
	out := storageIdentity{ID: "v1:" + hex.EncodeToString(sum[:]), Strength: controlplane.IdentityStrong}
	if weak {
		out.Strength = controlplane.IdentityWeak
	}
	return out, nil
}

// designatorIsSlotName: a vendor-specific or T10 designator whose text is a
// slot name. Anything else, binary payloads included, is not.
func designatorIsSlotName(codeSet, kind uint32, payload []byte) bool {
	if codeSet != storageIDCodeSetASCII && codeSet != storageIDCodeSetUTF8 {
		return false
	}
	switch kind {
	case storageIDTypeVendorSpecific:
		return isStorageSlotName(string(payload))
	case storageIDTypeVendorID:
		return len(payload) > 8 && isStorageSlotName(string(payload[8:]))
	}
	return false
}

func isStorageSlotName(s string) bool {
	return storageSlotName.MatchString(strings.TrimSpace(strings.TrimRight(s, "\x00")))
}

// storageDeviceDescriptorIdentity reads a STORAGE_DEVICE_DESCRIPTOR, the
// fallback for devices with no page-0x83 designators: vendor, product and
// serial. A missing serial is no hardware identity (the caller falls back to
// diskLayoutIdentity); a serial that is a slot name is a weak one.
func storageDeviceDescriptorIdentity(data []byte) (storageIdentity, error) {
	var zero storageIdentity
	if len(data) < 36 {
		return zero, errors.New("short storage device descriptor")
	}
	field := func(offset int) string {
		start := int(binary.LittleEndian.Uint32(data[offset:]))
		if start <= 0 || start >= len(data) {
			return ""
		}
		end := bytes.IndexByte(data[start:], 0)
		if end < 0 {
			return ""
		}
		return strings.TrimSpace(string(data[start : start+end]))
	}
	vendor, product, serial := field(12), field(16), field(24)
	if serial == "" || vendor == "" || product == "" {
		return zero, errors.New("device lacks a persistent hardware identity")
	}
	sum := sha256.Sum256([]byte("serial-v1\x00" + vendor + "\x00" + product + "\x00" + serial))
	out := storageIdentity{ID: "v1:" + hex.EncodeToString(sum[:]), Strength: controlplane.IdentityStrong}
	if isStorageSlotName(serial) {
		out.Strength = controlplane.IdentityWeak
	}
	return out, nil
}

// diskLayoutIdentity names a disk that reports no hardware identifier. The ID
// is derived from the disk GUID (or MBR signature) alone and is reported
// weak, so it is matched like any weak identity: by that GUID, size and
// partition layout, never by this ID. A disk with no GUID has nothing to
// derive it from and stays unidentified.
func diskLayoutIdentity(diskID string) (storageIdentity, bool) {
	diskID = strings.TrimSpace(diskID)
	if diskID == "" {
		return storageIdentity{}, false
	}
	sum := sha256.Sum256([]byte("disk-layout-v1\x00" + diskID))
	return storageIdentity{ID: "v1:" + hex.EncodeToString(sum[:]), Strength: controlplane.IdentityWeak}, true
}

// withStorageIdentity gives a discovered device its identity: what the open
// device reported, or when it reported nothing, the layout identity. A device
// with neither keeps an empty ID and cannot be approved.
func withStorageIdentity(d controlplane.StorageDevice, hw storageIdentity, hwErr error) controlplane.StorageDevice {
	if hwErr == nil && hw.ID != "" {
		d.ID, d.IdentityStrength = hw.ID, hw.Strength
		return d
	}
	if id, ok := diskLayoutIdentity(d.DiskID); ok {
		d.ID, d.IdentityStrength = id.ID, id.Strength
		return d
	}
	d.ID, d.IdentityStrength = "", ""
	return d
}

// openedStorageIdentityMatches decides whether the identity read from the
// open device agrees with the device approval resolved to. A device whose ID
// is its layout identity reports no hardware identifier; there is nothing to
// compare and the layout check on the same handle decides. Any other device
// must report the ID it was found with.
func openedStorageIdentityMatches(expected controlplane.StorageDevice, hw storageIdentity, hwErr error) error {
	if layout, ok := diskLayoutIdentity(expected.DiskID); ok && expected.ID == layout.ID && hwErr != nil {
		return nil
	}
	if hwErr != nil {
		return hwErr
	}
	if hw.ID != expected.ID {
		return errors.New("opened disk hardware identity differs from approved device")
	}
	return nil
}

// storageModelLimit matches the server's limit for the field; see storageModel.
const storageModelLimit = 128

// storageModel is the display name sent with a device (Get-Disk FriendlyName).
// The server refuses a WHOLE storage report over one control character or an
// overlong field, so a strange name must not cost the machine its storage
// evidence: control characters become spaces, runs of white space collapse,
// and the result is cut to storageModelLimit bytes on a rune boundary.
func storageModel(s string) string {
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || r == utf8.RuneError {
			return ' '
		}
		return r
	}, s)), " ")
	if len(s) <= storageModelLimit {
		return s
	}
	cut := storageModelLimit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut])
}
