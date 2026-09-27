//go:build windows

package main

import (
	"controlplane"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// Query identifiers on the actual open device, not a path that may have
// been reassigned. STORAGE_DEVICE_ID_DESCRIPTOR contains SCSI VPD identifiers;
// storage_identity_parse.go reads them and says how much they are worth.
func storageHandleID(h windows.Handle) (storageIdentity, error) {
	query := make([]byte, 12)
	binary.LittleEndian.PutUint32(query, 2) // StorageDeviceIdProperty
	data := make([]byte, 65536)
	var n uint32
	err := windows.DeviceIoControl(h, 0x2d1400, &query[0], uint32(len(query)), &data[0], uint32(len(data)), &n, nil)
	if err == nil && n <= uint32(len(data)) {
		if id, e := storageDescriptorIdentity(data[:n]); e == nil {
			return id, nil
		}
	}
	// Devices without VPD identifiers may expose a vendor/product/unit serial.
	// A missing serial is an error; a layout signature alone is not hardware ID.
	binary.LittleEndian.PutUint32(query, 0) // StorageDeviceProperty
	err = windows.DeviceIoControl(h, 0x2d1400, &query[0], uint32(len(query)), &data[0], uint32(len(data)), &n, nil)
	if err != nil {
		return storageIdentity{}, fmt.Errorf("device does not report a persistent identity: %w", err)
	}
	if n > uint32(len(data)) {
		return storageIdentity{}, errors.New("short storage device descriptor")
	}
	return storageDeviceDescriptorIdentity(data[:n])
}
func storagePathID(path string) (storageIdentity, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return storageIdentity{}, err
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return storageIdentity{}, err
	}
	defer windows.CloseHandle(h)
	return storageHandleID(h)
}

func verifyOpenedStorageDevice(handle uintptr, expected controlplane.StorageDevice) error {
	h := windows.Handle(handle)
	id, err := storageHandleID(h)
	if err != nil {
		return err
	}
	// expected is the device ResolveStorageBinding found in this observation,
	// so for a weak identity found in another slot this is its CURRENT id.
	if id.ID != expected.ID {
		return errors.New("opened disk hardware identity differs from approved device")
	}
	// Cross-reference disk/partition identities through this same handle.
	data := make([]byte, 48+144*128)
	var n uint32
	if err = windows.DeviceIoControl(h, IOCTL_DISK_GET_DRIVE_LAYOUT_EX, nil, 0, &data[0], uint32(len(data)), &n, nil); err != nil {
		return err
	}
	if n < 48 || n > uint32(len(data)) {
		return errors.New("short opened-disk partition layout")
	}
	data = data[:n]
	style := binary.LittleEndian.Uint32(data)
	count := int(binary.LittleEndian.Uint32(data[4:]))
	if count > 128 || 48+144*count > len(data) {
		return errors.New("invalid opened-disk partition count")
	}
	guid := func(b []byte) string {
		return fmt.Sprintf("%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x", binary.LittleEndian.Uint32(b), binary.LittleEndian.Uint16(b[4:]), binary.LittleEndian.Uint16(b[6:]), b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
	}
	diskID := ""
	if style == 1 {
		diskID = guid(data[8:24])
	} else if style == 0 {
		diskID = fmt.Sprintf("mbr:%08x", binary.LittleEndian.Uint32(data[8:]))
	}
	if diskID == "" || diskID != expected.DiskID {
		return errors.New("opened disk layout identity differs from approved disk")
	}
	found := 0
	for i := 0; i < count; i++ {
		p := data[48+i*144:]
		if binary.LittleEndian.Uint32(p[24:]) == 0 {
			continue
		}
		offset, size := binary.LittleEndian.Uint64(p[8:]), binary.LittleEndian.Uint64(p[16:])
		partID := ""
		if style == 1 {
			partID = guid(p[48:64])
		} else {
			partID = fmt.Sprintf("%s:%d", diskID, offset)
		}
		matched := false
		for _, want := range expected.Partitions {
			if want.ID == partID && want.Offset == offset && want.Size == size {
				matched = true
				break
			}
		}
		if !matched {
			return errors.New("opened disk partition differs from approved baseline")
		}
		found++
	}
	if found != len(expected.Partitions) {
		return errors.New("opened disk partition count differs from approved baseline")
	}
	return nil
}
