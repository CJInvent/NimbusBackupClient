package controlplane

import (
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
