//go:build windows

package main

import (
	"path/filepath"
	"snapshot"
	"testing"
)

func TestImageConsumerSnapshotIdentity(t *testing.T) {
	const volume = `\\?\Volume{11111111-1111-1111-1111-111111111111}\`
	parts := []Partition{{RequiresVSS: true, VolumePath: volume}, {}}
	paths := imageSnapshotPaths(parts)
	if len(paths) != 1 || paths[0] != volume {
		t.Fatalf("snapshot requested wrong volume: %q", paths)
	}
	// CreateVSSSnapshot keys its result by filepath.Abs(requested path).
	actualKey, err := filepath.Abs(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	const shadow = `\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy11\`
	snapshots := map[string]snapshot.SnapShot{actualKey: {Valid: true, ObjectPath: shadow}}
	got, err := imagePartitionSnapshot(parts[0], snapshots)
	if err != nil || got.ObjectPath != shadow {
		t.Fatalf("created snapshot unavailable to image reader: %+v %v", got, err)
	}
	if _, err := imagePartitionSnapshot(Partition{VolumePath: "missing"}, snapshots); err == nil {
		t.Fatal("missing snapshot accepted")
	}
	snapshots[actualKey] = snapshot.SnapShot{ObjectPath: shadow}
	if _, err := imagePartitionSnapshot(parts[0], snapshots); err == nil {
		t.Fatal("invalid snapshot accepted")
	}
}
