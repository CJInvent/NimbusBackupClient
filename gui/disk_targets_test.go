package main

import "testing"

// Resolving a job's disk targets — see disk_targets.go.
//
// The defect being pinned: a managed job created in the portal with drive "C"
// failed at run time with `invalid physical drive path: C`, because the portal
// collected letters and the image engine wanted `\\.\PhysicalDriveN` and
// nothing translated. Every image job created through the portal would have
// failed the same way.

func disks() []PhysicalDiskInfo {
	return []PhysicalDiskInfo{
		{DiskNumber: 0, Letters: []string{"C:", "E:"}, Path: `\\.\PhysicalDrive0`},
		{DiskNumber: 1, Letters: []string{"D:"}, Path: `\\.\PhysicalDrive1`},
		{DiskNumber: 2, Letters: nil, Path: `\\.\PhysicalDrive2`},
	}
}

func TestALetterResolvesToItsDisk(t *testing.T) {
	got, err := resolveDiskTargets([]string{"C"}, disks())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != `\\.\PhysicalDrive0` {
		t.Fatalf("got %v, want [\\\\.\\PhysicalDrive0]", got)
	}
}

func TestTheShapesAnOperatorActuallyTypes(t *testing.T) {
	for _, in := range []string{"C", "c", "C:", "c:", `C:\`, " C: "} {
		got, err := resolveDiskTargets([]string{in}, disks())
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got[0] != `\\.\PhysicalDrive0` {
			t.Fatalf("%q resolved to %v", in, got)
		}
	}
}

func TestTwoLettersOnOneDiskAreOneImage(t *testing.T) {
	// C: and E: share disk 0. Imaging it twice into one snapshot is not a
	// tidiness question -- it is duplicated work and a corrupt-looking result.
	got, err := resolveDiskTargets([]string{"C", "E"}, disks())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %v, want one disk", got)
	}
}

func TestDevicePathsPassThrough(t *testing.T) {
	// The agent's own picker has a real disk in hand and offers no letter for
	// an unlettered one, so it must keep working.
	got, err := resolveDiskTargets([]string{`\\.\PhysicalDrive2`}, disks())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0] != `\\.\PhysicalDrive2` {
		t.Fatalf("got %v", got)
	}
}

func TestOrderIsTheOrderTheDisksWereNamed(t *testing.T) {
	got, err := resolveDiskTargets([]string{"D", "C"}, disks())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0] != `\\.\PhysicalDrive1` || got[1] != `\\.\PhysicalDrive0` {
		t.Fatalf("got %v", got)
	}
}

func TestALetterThatIsNotHereSaysWhatIS(t *testing.T) {
	// "Z is not a drive on this machine" sends a tech hunting a missing disk.
	// Listing what the machine actually has usually shows them the answer.
	_, err := resolveDiskTargets([]string{"Z"}, disks())
	if err == nil {
		t.Fatal("a drive that is not present must not resolve")
	}
	for _, want := range []string{"Z", "disk 0", "C:", "disk 1", "D:"} {
		if !containsSub(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err.Error(), want)
		}
	}
}

func TestGarbageIsRefusedRatherThanGuessed(t *testing.T) {
	for _, in := range []string{"C:\\Users", "PhysicalDrive0", "CD", "1"} {
		if _, err := resolveDiskTargets([]string{in}, disks()); err == nil {
			t.Fatalf("%q was accepted", in)
		}
	}
}

func TestNamingNothingIsItsOwnError(t *testing.T) {
	// Distinct from naming something absent: one is an empty form, the other
	// is a job pointed at the wrong machine.
	_, err := resolveDiskTargets([]string{"  ", ""}, disks())
	if err == nil {
		t.Fatal("empty targets must not resolve")
	}
	if !containsSub(err.Error(), "NB-2002") {
		t.Fatalf("want NB-2002 for an empty target list, got %q", err.Error())
	}
}

func containsSub(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
