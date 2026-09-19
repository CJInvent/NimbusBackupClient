//go:build windows

package main

import (
	"context"
	"controlplane"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// Fixed read-only query. No job/portal input is interpolated into shell code.
// Storage UniqueId is the device identifier; disk/partition GUIDs independently
// detect cloned/replaced media. WindowsDirectory comes from the Win32 API.
const storageInventoryScript = `$ErrorActionPreference='Stop'
$items=@(Get-Disk | ForEach-Object {
 $d=$_
 $parts=@(Get-Partition -DiskNumber $d.Number | ForEach-Object {
  [ordered]@{ID=[string]$_.Guid;Offset=[uint64]$_.Offset;Size=[uint64]$_.Size;Boot=[bool]$_.IsBoot;System=[bool]$_.IsSystem;Letter=[string]$_.DriveLetter}
 })
 [ordered]@{Number=[int]$d.Number;UniqueID=[string]$d.UniqueId;Format=[string]$d.UniqueIdFormat;DiskID=[string]$d.Guid;Signature=[uint32]$d.Signature;Style=[string]$d.PartitionStyle;Size=[int64]$d.Size;Boot=[bool]$d.IsBoot;System=[bool]$d.IsSystem;Partitions=$parts}
})
ConvertTo-Json -InputObject $items -Depth 5 -Compress`

type nativeStorageDisk struct {
	Number                          int
	UniqueID, Format, DiskID, Style string
	Signature                       uint32
	Size                            int64
	Boot, System                    bool
	Partitions                      []nativeStoragePartition
}
type nativeStoragePartition struct {
	ID           string
	Offset, Size uint64
	Boot, System bool
	Letter       string
}

func discoverStorageDevices() ([]controlplane.StorageDevice, error) {
	windowsDir, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		return nil, fmt.Errorf("identify running Windows directory: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", storageInventoryScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read Windows storage identity: %w", err)
	}
	if len(out) > 1024*1024 {
		return nil, fmt.Errorf("storage inventory exceeds limit")
	}
	var raw []nativeStorageDisk
	if err = json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode Windows storage identity: %w", err)
	}
	devices, err := buildStorageDevices(raw, filepath.VolumeName(windowsDir))
	if err != nil {
		return nil, err
	}
	for i := range devices {
		devices[i].ID, err = storagePathID(devices[i].Path)
		if err != nil {
			// An unselected device with no trustworthy ID is not authority to
			// stop backups of an independently verified disk. It remains
			// visible but unapprovable; a bound device disappearing still latches.
			devices[i].ID = ""
		}
	}
	return devices, nil
}

func buildStorageDevices(raw []nativeStorageDisk, windowsVolume string) ([]controlplane.StorageDevice, error) {
	result := make([]controlplane.StorageDevice, 0, len(raw))
	systemDisk := -1
	systemCount := 0
	for _, d := range raw {
		if d.System {
			systemDisk = d.Number
			systemCount++
		}
	}
	for _, d := range raw {
		id := ""
		if strings.TrimSpace(d.UniqueID) != "" {
			sum := sha256.Sum256([]byte("nimbus-storage-v1\x00" + strings.TrimSpace(d.Format) + "\x00" + strings.TrimSpace(d.UniqueID)))
			id = "v1:" + hex.EncodeToString(sum[:])
		}
		diskID := strings.ToLower(strings.Trim(d.DiskID, "{}"))
		if d.Style == "MBR" && d.Signature != 0 {
			diskID = fmt.Sprintf("mbr:%08x", d.Signature)
		}
		entry := controlplane.StorageDevice{ID: id, DiskID: diskID, Path: fmt.Sprintf(`\\.\PhysicalDrive%d`, d.Number), SizeBytes: d.Size, Boot: d.Boot}
		for _, p := range d.Partitions {
			partID := strings.ToLower(strings.Trim(p.ID, "{}"))
			if d.Style == "MBR" {
				partID = fmt.Sprintf("mbr:%08x:%d", d.Signature, p.Offset)
			}

			entry.Partitions = append(entry.Partitions, controlplane.StoragePartition{ID: partID, Offset: p.Offset, Size: p.Size, Boot: p.Boot, System: p.System})
			if p.Boot && d.Boot && strings.EqualFold(p.Letter+":", windowsVolume) && systemCount == 1 && systemDisk == d.Number {
				entry.BootVerified = true
			}
		}
		result = append(result, entry)
	}
	return result, nil
}
