package main

import (
	"errors"
	"testing"
	"time"
)

// The disk inventory cache — see disk_inventory.go.

func fixedDisks() []PhysicalDiskInfo {
	return []PhysicalDiskInfo{
		{DiskNumber: 0, SizeBytes: 500, Letters: []string{"C:"}, Label: "Disque 0"},
		{DiskNumber: 1, SizeBytes: 900, Letters: nil, Label: "Disque 1"},
	}
}

func TestDisksAreReportedWithoutTheAgentsLabel(t *testing.T) {
	c := &diskInvCache{list: func() ([]PhysicalDiskInfo, error) { return fixedDisks(), nil },
		now: time.Now, ttl: time.Minute}
	got := c.get()
	if len(got) != 2 {
		t.Fatalf("got %d disks, want 2", len(got))
	}
	if got[0].DiskNumber != 0 || got[0].SizeBytes != 500 || got[0].Letters[0] != "C:" {
		t.Fatalf("disk 0 came through as %+v", got[0])
	}
	// A disk with no letters is a real disk, and null is not a shape the
	// server should have to distinguish from empty.
	if got[1].Letters == nil || len(got[1].Letters) != 0 {
		t.Fatalf("unlettered disk reported letters as %#v, want []", got[1].Letters)
	}
}

func TestTheDisksAreNotEnumeratedOnEveryCheckin(t *testing.T) {
	calls := 0
	now := time.Now()
	c := &diskInvCache{
		list: func() ([]PhysicalDiskInfo, error) { calls++; return fixedDisks(), nil },
		now:  func() time.Time { return now },
		ttl:  15 * time.Minute,
	}
	for i := 0; i < 10; i++ {
		c.get()
		now = now.Add(2 * time.Minute) // a check-in cycle
	}
	// 20 minutes of check-ins, one TTL expiry: two reads, not ten.
	if calls != 2 {
		t.Fatalf("enumerated %d times over 20 minutes of check-ins, want 2", calls)
	}
}

func TestAFailedEnumerationKeepsTheLastGoodList(t *testing.T) {
	// An absent list means "no reading" to the server. If a failure cleared
	// the cache, one bad read would make the portal forget a machine's disks
	// — the exact thing the absent-vs-empty distinction exists to prevent.
	ok := true
	now := time.Now()
	c := &diskInvCache{
		list: func() ([]PhysicalDiskInfo, error) {
			if ok {
				return fixedDisks(), nil
			}
			return nil, errors.New("disk enumeration failed")
		},
		now: func() time.Time { return now },
		ttl: time.Minute,
	}
	c.get()
	ok = false
	now = now.Add(2 * time.Minute)
	if got := c.get(); len(got) != 2 {
		t.Fatalf("after a failed read the cache held %d disks, want the previous 2", len(got))
	}
}

func TestNothingIsReportedBeforeAnyGoodReading(t *testing.T) {
	c := &diskInvCache{
		list: func() ([]PhysicalDiskInfo, error) { return nil, errors.New("nope") },
		now:  time.Now, ttl: time.Minute,
	}
	if got := c.get(); got != nil {
		t.Fatalf("got %#v, want nil — an empty list would claim this machine has no disks", got)
	}
}
