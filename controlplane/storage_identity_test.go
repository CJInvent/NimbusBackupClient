package controlplane

import (
	"encoding/json"
	"strings"
	"testing"
)

func storageFixture() StorageDevice {
	return StorageDevice{ID: "v1:" + strings.Repeat("a", 64), DiskID: "gpt:a", Path: "disk0", SizeBytes: 1000, Boot: true, BootVerified: true, Partitions: []StoragePartition{{ID: "partition-a", Offset: 10, Size: 990, Boot: true}}}
}
func TestStorageBindingFollowsIdentityAcrossRenumbering(t *testing.T) {
	expected := storageFixture()
	current := storageFixture()
	current.Path = "disk7"
	got, err := ResolveStorageBinding(StorageBinding{Target: "boot", Device: expected}, []StorageDevice{current})
	if err != nil || got.Path != "disk7" {
		t.Fatalf("got %+v %v", got, err)
	}
	if StorageObservation([]StorageDevice{expected}) != StorageObservation([]StorageDevice{current}) {
		t.Fatal("device renumbering changed identity")
	}
}
func TestStorageBindingRefusesReplacementAndAmbiguity(t *testing.T) {
	expected := storageFixture()
	for _, name := range []string{"missing", "hardware", "disk", "partition", "boot", "unverified", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			current := storageFixture()
			current.Partitions = append([]StoragePartition(nil), current.Partitions...)
			devices := []StorageDevice{current}
			switch name {
			case "missing":
				devices = nil
			case "hardware":
				devices[0].ID = "v1:hardware-b"
			case "disk":
				devices[0].DiskID = "gpt:b"
			case "partition":
				devices[0].Partitions[0].ID = "partition-b"
			case "boot":
				devices[0].Boot = false
			case "unverified":
				devices[0].BootVerified = false
			case "duplicate":
				devices = append(devices, current)
			}
			if _, err := ResolveStorageBinding(StorageBinding{Target: "boot", Device: expected}, devices); err == nil {
				t.Fatal("unsafe target accepted")
			}
		})
	}
}
func TestStorageApprovalRejectsStaleObservation(t *testing.T) {
	d := storageFixture()
	st := StorageStatus{Devices: []StorageDevice{d}, Revision: 4}
	approval := StorageApproval{Revision: 5, Observation: StorageObservation(st.Devices), Bindings: []StorageBinding{{Target: "boot", Device: d}}}
	if err := ValidateStorageApproval(approval, st); err != nil {
		t.Fatal(err)
	}
	approval.Revision = 4
	if err := ValidateStorageApproval(approval, st); err == nil {
		t.Fatal("replay accepted")
	}
	approval.Revision = 5
	st.Devices[0].DiskID = "replaced"
	if err := ValidateStorageApproval(approval, st); err == nil {
		t.Fatal("stale observation accepted")
	}
}

func TestStorageApprovalRejectsIncompleteEvidence(t *testing.T) {
	for _, kind := range []string{"empty-partition", "duplicate-partition", "overlap", "beyond-disk", "missing-hardware"} {
		t.Run(kind, func(t *testing.T) {
			d := storageFixture()
			switch kind {
			case "empty-partition":
				d.Partitions[0].ID = ""
			case "duplicate-partition":
				d.Partitions = append(d.Partitions, d.Partitions[0])
			case "overlap":
				d.Partitions = append(d.Partitions, StoragePartition{ID: "other", Offset: 20, Size: 30})
			case "beyond-disk":
				d.Partitions[0].Size = 1000
			case "missing-hardware":
				d.ID = ""
			}
			st := StorageStatus{Devices: []StorageDevice{d}}
			ap := StorageApproval{Revision: 1, Observation: StorageObservation(st.Devices), Bindings: []StorageBinding{{Target: "boot", Device: d}}}
			if ValidateStorageApproval(ap, st) == nil {
				t.Fatal("incomplete evidence approved")
			}
		})
	}
}

// F-39/F-41 (docs/V4-BETA-FIXES.md §5): what a binding may match depends on
// the identity strength recorded when it was approved.
func weakFixture(id string) StorageDevice {
	d := storageFixture()
	d.ID = "v1:" + strings.Repeat(id, 64)
	d.DiskID = "gpt:w"
	d.Boot, d.BootVerified = false, false
	d.IdentityStrength = IdentityWeak
	return d
}

func TestStorageWeakIdentityFollowsTheDiskToAnotherSlot(t *testing.T) {
	approved := weakFixture("1")
	moved := weakFixture("2") // same disk, new slot: its slot-derived id changed
	moved.Path = "disk3"
	binding := StorageBinding{Target: "device:" + approved.ID, Device: approved}
	got, err := ResolveStorageBinding(binding, []StorageDevice{storageFixture(), moved})
	if err != nil {
		t.Fatalf("moved weak disk refused: %v", err)
	}
	if got.ID != moved.ID || got.Path != "disk3" {
		t.Fatalf("resolved %+v, want the moved device", got)
	}
}

func TestStorageWeakIdentityStillChecksTheDisk(t *testing.T) {
	approved := weakFixture("1")
	binding := StorageBinding{Target: "device:" + approved.ID, Device: approved}
	for _, name := range []string{"size", "partition", "guid", "twin", "twin-strong"} {
		t.Run(name, func(t *testing.T) {
			current := weakFixture("2")
			current.Partitions = append([]StoragePartition(nil), current.Partitions...)
			devices := []StorageDevice{current}
			switch name {
			case "size":
				devices[0].SizeBytes++
			case "partition":
				devices[0].Partitions[0].Size--
			case "guid":
				devices[0].DiskID = "gpt:other"
			case "twin": // a clone attached beside its original: same GUID twice
				devices = append(devices, weakFixture("3"))
			case "twin-strong":
				twin := weakFixture("3")
				twin.IdentityStrength = IdentityStrong
				devices = append(devices, twin)
			}
			if _, err := ResolveStorageBinding(binding, devices); err == nil {
				t.Fatal("unsafe weak target accepted")
			}
		})
	}
}

func TestStorageStrongIdentityRefusesACloneWithAnotherSerial(t *testing.T) {
	approved := weakFixture("1")
	approved.IdentityStrength = IdentityStrong
	clone := approved
	clone.ID = "v1:" + strings.Repeat("c", 64) // same bytes, different serial
	binding := StorageBinding{Target: "device:" + approved.ID, Device: approved}
	if _, err := ResolveStorageBinding(binding, []StorageDevice{clone}); err == nil {
		t.Fatal("clone with a different serial accepted for a strong identity")
	}
}

func TestStorageStrengthChangeNeedsApproval(t *testing.T) {
	for _, flip := range [][2]string{{IdentityWeak, IdentityStrong}, {IdentityStrong, IdentityWeak}, {IdentityWeak, ""}} {
		approved := weakFixture("1")
		approved.IdentityStrength = flip[0]
		current := approved
		current.IdentityStrength = flip[1]
		_, err := ResolveStorageBinding(StorageBinding{Target: "device:" + approved.ID, Device: approved}, []StorageDevice{current})
		if err == nil || !strings.Contains(err.Error(), "approve the storage again") {
			t.Errorf("%s -> %q: got %v, want a re-approval refusal", flip[0], flip[1], err)
		}
	}
}

// Bindings approved before strengths existed resolve exactly as they did:
// by id, whatever the device now reports. Approving again records it.
func TestStorageBindingWithoutRecordedStrength(t *testing.T) {
	approved := weakFixture("1")
	approved.IdentityStrength = ""
	binding := StorageBinding{Target: "device:" + approved.ID, Device: approved}
	same := weakFixture("1")
	if _, err := ResolveStorageBinding(binding, []StorageDevice{same}); err != nil {
		t.Fatalf("pre-F-39 binding broke on upgrade: %v", err)
	}
	if _, err := ResolveStorageBinding(binding, []StorageDevice{weakFixture("2")}); err == nil {
		t.Fatal("pre-F-39 binding matched a different id")
	}
}

func TestStorageObservationUnchangedWithoutNewFields(t *testing.T) {
	b, _ := json.Marshal(storageFixture())
	if strings.Contains(string(b), "identity_strength") || strings.Contains(string(b), "model") {
		t.Fatalf("an unchanged pre-F-39 device serializes differently: %s", b)
	}
	d := storageFixture()
	d.IdentityStrength = "medium"
	st := StorageStatus{Devices: []StorageDevice{d}}
	ap := StorageApproval{Revision: 1, Observation: StorageObservation(st.Devices), Bindings: []StorageBinding{{Target: "boot", Device: d}}}
	if ValidateStorageApproval(ap, st) == nil {
		t.Fatal("unknown identity strength approved")
	}
}
