package main

import "fmt"

// snapshotPadding permits only a known volume-to-partition tail difference.
// A larger snapshot is wrong evidence, never an unsigned padding calculation.
func snapshotPadding(partitionBytes uint64, snapshotBytes int64) (uint64, error) {
	if snapshotBytes <= 0 || uint64(snapshotBytes) > partitionBytes {
		return 0, fmt.Errorf("snapshot length %d is outside partition length %d", snapshotBytes, partitionBytes)
	}
	return partitionBytes - uint64(snapshotBytes), nil
}
