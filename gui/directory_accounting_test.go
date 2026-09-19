package main

import (
	"pbscommon"
	"testing"
)

func TestDirectoryAccountingIncludesSmallArchiveWithoutProgress(t *testing.T) {
	// Actual live one-MiB proof archive length, below the ten-MiB progress cadence.
	files := []pbscommon.File{{Filename: "backup.pxar.didx", Size: 1049267}, {Filename: "catalog.pcat1.didx", Size: 99}, {Filename: "nimbus-status.json.blob", Size: 250}, {Filename: "rsa-encrypted.key.blob", Size: 524}}
	if got := directoryManifestBytes(files); got != 1049267 {
		t.Fatalf("committed bytes=%d", got)
	}
	files = append(files, pbscommon.File{Filename: "other.pxar.didx", Size: 4096})
	if got := directoryManifestBytes(files); got != 1053363 {
		t.Fatalf("multiple archives=%d", got)
	}
}
