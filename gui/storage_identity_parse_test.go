package main

import (
	"controlplane"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// The test VM's own device ids, recorded in docs/V4-BETA-EVIDENCE-2026-09-23.md
// (2026-09-24, F-39) when the 8 GiB test disk was moved from scsi1 to scsi2.
// They were computed by the pre-F-39 client on the real device, so they pin
// both what QEMU puts in page 0x83 (one ASCII vendor-specific designator
// holding the drive id, nothing else) and that F-39 left the id unchanged.
const (
	evidenceIDScsi1 = "v1:9cb86b348c3f4caa7933a77a7901db87c412d332012539f79749adf8cac33880"
	evidenceIDScsi2 = "v1:d0bbbea892109cb1545da6e3bc949ae189140c71710efa17aba92c793cfdf56d"
)

type designator struct {
	codeSet, kind, association uint32
	payload                    []byte
}

// deviceIDDescriptor lays out STORAGE_DEVICE_ID_DESCRIPTOR as ntddstor.h
// defines it: Version, Size, NumberOfIdentifiers, then STORAGE_IDENTIFIERs
// (CodeSet, Type, IdentifierSize u16, NextOffset u16, Association, bytes).
func deviceIDDescriptor(ds ...designator) []byte {
	var body []byte
	for _, d := range ds {
		rec := make([]byte, 16, 16+len(d.payload)+3)
		binary.LittleEndian.PutUint32(rec[0:], d.codeSet)
		binary.LittleEndian.PutUint32(rec[4:], d.kind)
		binary.LittleEndian.PutUint16(rec[8:], uint16(len(d.payload)))
		rec = append(rec, d.payload...)
		for len(rec)%4 != 0 {
			rec = append(rec, 0)
		}
		binary.LittleEndian.PutUint16(rec[10:], uint16(len(rec)))
		binary.LittleEndian.PutUint32(rec[12:], d.association)
		body = append(body, rec...)
	}
	head := make([]byte, 12)
	binary.LittleEndian.PutUint32(head[0:], 1)
	binary.LittleEndian.PutUint32(head[4:], uint32(12+len(body)))
	binary.LittleEndian.PutUint32(head[8:], uint32(len(ds)))
	return append(head, body...)
}

func ascii(kind uint32, s string) designator {
	return designator{codeSet: storageIDCodeSetASCII, kind: kind, payload: []byte(s)}
}

func TestStorageIdentityQemuWithoutSerialIsWeakAndKeepsItsID(t *testing.T) {
	for slot, want := range map[string]string{"drive-scsi1": evidenceIDScsi1, "drive-scsi2": evidenceIDScsi2} {
		got, err := storageDescriptorIdentity(deviceIDDescriptor(ascii(storageIDTypeVendorSpecific, slot)))
		if err != nil {
			t.Fatalf("%s: %v", slot, err)
		}
		if got.ID != want {
			t.Errorf("%s: id %s, the test VM recorded %s (F-39 must not change existing ids)", slot, got.ID, want)
		}
		if got.Strength != controlplane.IdentityWeak {
			t.Errorf("%s: strength %q, want weak (the identity is the slot)", slot, got.Strength)
		}
	}
}

func TestStorageIdentityStrength(t *testing.T) {
	naa := designator{codeSet: 1, kind: 3, payload: []byte{0x50, 0x00, 0xc5, 0x00, 0xa1, 0xb2, 0xc3, 0xd4}}
	eui := designator{codeSet: 1, kind: 2, payload: []byte{1, 2, 3, 4, 5, 6, 7, 8}}
	portSlot := ascii(storageIDTypeVendorSpecific, "drive-scsi9")
	portSlot.association = 1
	cases := []struct {
		name string
		ds   []designator
		want string
	}{
		{"serial= set", []designator{ascii(0, "NBCTEST0001")}, controlplane.IdentityStrong},
		{"wwn= beside the slot name", []designator{ascii(0, "drive-scsi1"), naa}, controlplane.IdentityStrong},
		{"EUI-64 only", []designator{eui}, controlplane.IdentityStrong},
		{"T10 slot name", []designator{ascii(1, "QEMU    drive-scsi3")}, controlplane.IdentityWeak},
		{"T10 serial", []designator{ascii(1, "QEMU    S3Z9NB0K123456")}, controlplane.IdentityStrong},
		{"UTF-8 slot name", []designator{{codeSet: storageIDCodeSetUTF8, payload: []byte("drive-virtio0")}}, controlplane.IdentityWeak},
		{"binary vendor-specific", []designator{{codeSet: 1, payload: []byte("drive-scsi1")}}, controlplane.IdentityStrong},
		{"slot name inside a longer serial", []designator{ascii(0, "xdrive-scsi1")}, controlplane.IdentityStrong},
		{"port designator ignored", []designator{ascii(0, "drive-sata0"), portSlot}, controlplane.IdentityWeak},
		{"two slot names", []designator{ascii(0, "drive-ide0"), ascii(1, "QEMU    drive-ide0")}, controlplane.IdentityWeak},
	}
	for _, c := range cases {
		got, err := storageDescriptorIdentity(deviceIDDescriptor(c.ds...))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.Strength != c.want {
			t.Errorf("%s: strength %q, want %q", c.name, got.Strength, c.want)
		}
	}
}

func TestStorageIdentityPortOnlyIsNoIdentity(t *testing.T) {
	d := ascii(0, "drive-scsi1")
	d.association = 1
	if _, err := storageDescriptorIdentity(deviceIDDescriptor(d)); err == nil {
		t.Fatal("a descriptor with no device-associated designator produced an identity")
	}
}

// STORAGE_DEVICE_DESCRIPTOR: VendorIdOffset at 12, ProductIdOffset at 16,
// SerialNumberOffset at 24, each an offset to a NUL-terminated string.
func deviceDescriptor(vendor, product, serial string) []byte {
	b := make([]byte, 40)
	put := func(off int, s string) {
		if s == "" {
			return
		}
		binary.LittleEndian.PutUint32(b[off:], uint32(len(b)))
		b = append(b, append([]byte(s), 0)...)
	}
	put(12, vendor)
	put(16, product)
	put(24, serial)
	return b
}

func TestStorageSerialFallbackStrength(t *testing.T) {
	got, err := storageDeviceDescriptorIdentity(deviceDescriptor("QEMU", "QEMU HARDDISK", "drive-scsi4"))
	if err != nil || got.Strength != controlplane.IdentityWeak {
		t.Fatalf("slot-shaped serial: %+v %v", got, err)
	}
	got, err = storageDeviceDescriptorIdentity(deviceDescriptor("WDC", "WD40EFRX", "WD-WCC4E1234567"))
	if err != nil || got.Strength != controlplane.IdentityStrong {
		t.Fatalf("real serial: %+v %v", got, err)
	}
	if _, err = storageDeviceDescriptorIdentity(deviceDescriptor("WDC", "WD40EFRX", "")); err == nil {
		t.Fatal("no serial produced an identity")
	}
}

func TestStorageModelNeverBreaksTheReport(t *testing.T) {
	if got := storageModel("  QEMU\tQEMU\x00HARDDISK \x7f "); got != "QEMU QEMU HARDDISK" {
		t.Fatalf("control characters: %q", got)
	}
	long := storageModel(strings.Repeat("é", 100))
	if len(long) > storageModelLimit || !utf8.ValidString(long) {
		t.Fatalf("long model: %d bytes, valid=%v", len(long), utf8.ValidString(long))
	}
}

// Captured descriptors from the test VM (ledger prerequisite C0, captured by
// docs/reverify-2026-09-27/C0-Capture.ps1 -Part Storage in NimbusControl):
// gui/testdata/storage/<label>.json is that script's disks.json and
// <label>.want.json maps disk number to the expected strength. None are
// committed yet; until they are, the evidence ids above are the pin.
func TestStorageIdentityCapturedDescriptors(t *testing.T) {
	wants, _ := filepath.Glob(filepath.Join("testdata", "storage", "*.want.json"))
	if len(wants) == 0 {
		t.Skip("no captured storage descriptors yet (ledger C0)")
	}
	for _, wf := range wants {
		var want map[string]string
		mustReadJSON(t, wf, &want)
		var disks []struct {
			Number int    `json:"number"`
			IDHex  string `json:"device_id_descriptor_hex"`
			DevHex string `json:"device_descriptor_hex"`
		}
		mustReadJSON(t, strings.TrimSuffix(wf, ".want.json")+".json", &disks)
		for _, d := range disks {
			expected, ok := want[strings.TrimSpace(jsonNumber(d.Number))]
			if !ok {
				continue
			}
			got := storageIdentity{}
			if b, err := hex.DecodeString(d.IDHex); err == nil && len(b) > 0 {
				got, err = storageDescriptorIdentity(b)
				if err != nil {
					got = storageIdentity{}
				}
			}
			if got.ID == "" {
				if b, err := hex.DecodeString(d.DevHex); err == nil {
					got, _ = storageDeviceDescriptorIdentity(b)
				}
			}
			if got.Strength != expected {
				t.Errorf("%s disk %d: strength %q, want %q", filepath.Base(wf), d.Number, got.Strength, expected)
			}
		}
	}
}

func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func mustReadJSON(t *testing.T, path string, v interface{}) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(strings.TrimPrefix(string(b), "\ufeff")) // Set-Content -Encoding UTF8 writes a BOM
	if err = json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
