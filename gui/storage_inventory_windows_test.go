//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"os"
	"testing"
)

func TestStorageInventoryNonCBoot(t *testing.T) {
	raw := []nativeStorageDisk{{Number: 7, UniqueID: "disk-serial", Format: "vendor", DiskID: "disk-guid", Style: "GPT", Boot: true, System: true, Partitions: []nativeStoragePartition{{ID: "partition-guid", Boot: true, Letter: "W"}}}}
	got, err := buildStorageDevices(raw, "W:")
	if err != nil || len(got) != 1 || !got[0].BootVerified {
		t.Fatalf("non-C boot unresolved: %+v %v", got, err)
	}
	raw[0].Partitions[0].Letter = "C"
	got, err = buildStorageDevices(raw, "W:")
	if err != nil || got[0].BootVerified {
		t.Fatalf("wrong Windows partition trusted: %+v %v", got, err)
	}
}
func TestStorageInventoryLive(t *testing.T) {
	if os.Getenv("NIMBUS_STORAGE_ACCEPTANCE") != "1" {
		t.Skip("explicit live storage acceptance")
	}
	disks, err := discoverStorageDevices()
	if err != nil {
		t.Fatal(err)
	}
	boot := 0
	for _, d := range disks {
		if d.Boot {
			boot++
			if !d.BootVerified || d.ID == "" || d.DiskID == "" {
				t.Fatalf("boot evidence incomplete: %+v", d)
			}
		}
	}
	if boot != 1 {
		t.Fatalf("boot count %d", boot)
	}
	for _, d := range disks {
		ptr, err := windows.UTF16PtrFromString(d.Path)
		if err != nil {
			t.Fatal(err)
		}
		h, err := windows.CreateFile(ptr, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		err = verifyOpenedStorageDevice(uintptr(h), d)
		if err != nil {
			windows.CloseHandle(h)
			t.Fatal(err)
		}
		d.ID = "replacement"
		if err := verifyOpenedStorageDevice(uintptr(h), d); err == nil {
			windows.CloseHandle(h)
			t.Fatal("wrong device baseline accepted")
		}
		windows.CloseHandle(h)
	}
	t.Logf("verified %d disk(s), Windows/partition agreement and actual opened-handle identity", len(disks))
}
