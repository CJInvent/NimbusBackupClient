package main

import "testing"

func TestSnapshotPaddingRefusesWrongVolumeSize(t *testing.T) {
	for _, size := range []int64{-1, 0, 4097} {
		if _, err := snapshotPadding(4096, size); err == nil {
			t.Fatalf("unsafe length %d accepted", size)
		}
	}
	for _, c := range []struct {
		size int64
		pad  uint64
	}{{4096, 0}, {3584, 512}} {
		got, err := snapshotPadding(4096, c.size)
		if err != nil || got != c.pad {
			t.Fatalf("padding=%d %v", got, err)
		}
	}
}
