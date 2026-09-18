//go:build windows

package main

import (
	"fmt"
	"snapshot"
)

// Keep the enumerated volume GUID unchanged through request and lookup.
// Mount paths are complete paths, not bare drive letters; directory mounts
// must snapshot their own volume rather than the drive that hosts the mount.
func imageSnapshotPaths(parts []Partition) []string {
	paths := make([]string, 0)
	for _, p := range parts {
		if p.RequiresVSS {
			paths = append(paths, p.VolumePath)
		}
	}
	return paths
}

func imagePartitionSnapshot(p Partition, snapshots map[string]snapshot.SnapShot) (snapshot.SnapShot, error) {
	snap, ok := snapshots[p.VolumePath]
	if !ok || !snap.Valid || snap.ObjectPath == "" {
		return snapshot.SnapShot{}, fmt.Errorf("no valid VSS snapshot for volume %s", p.VolumePath)
	}
	return snap, nil
}
