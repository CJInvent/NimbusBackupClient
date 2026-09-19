//go:build windows

package main

import (
	"bytes"
	"controlplane"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/sys/windows"
)

// Query identifiers on the actual open device, not a path that may have
// been reassigned. STORAGE_DEVICE_ID_DESCRIPTOR contains SCSI VPD identifiers.
func storageHandleID(h windows.Handle) (string, error) {
	query := make([]byte, 12)
	binary.LittleEndian.PutUint32(query, 2) // StorageDeviceIdProperty
	data := make([]byte, 65536)
	var n uint32
	err := windows.DeviceIoControl(h, 0x2d1400, &query[0], uint32(len(query)), &data[0], uint32(len(data)), &n, nil)
	if err == nil && n <= uint32(len(data)) {
		if id, e := storageDescriptorID(data[:n]); e == nil {
			return id, nil
		}
	}
	// Devices without VPD identifiers may expose a vendor/product/unit serial.
	// A missing serial is an error; a layout signature alone is not hardware ID.
	binary.LittleEndian.PutUint32(query, 0) // StorageDeviceProperty
	err = windows.DeviceIoControl(h, 0x2d1400, &query[0], uint32(len(query)), &data[0], uint32(len(data)), &n, nil)
	if err != nil {
		return "", fmt.Errorf("device does not report a persistent identity: %w", err)
	}
	if n < 36 || n > uint32(len(data)) {
		return "", errors.New("short storage device descriptor")
	}
	data = data[:n]
	field := func(offset int) string {
		start := int(binary.LittleEndian.Uint32(data[offset:]))
		if start <= 0 || start >= len(data) {
			return ""
		}
		end := bytes.IndexByte(data[start:], 0)
		if end < 0 {
			return ""
		}
		return strings.TrimSpace(string(data[start : start+end]))
	}
	vendor, product, serial := field(12), field(16), field(24)
	if serial == "" || vendor == "" || product == "" {
		return "", errors.New("device lacks a persistent hardware identity")
	}
	sum := sha256.Sum256([]byte("serial-v1\x00" + vendor + "\x00" + product + "\x00" + serial))
	return "v1:" + hex.EncodeToString(sum[:]), nil
}
func storageDescriptorID(data []byte) (string, error) {
	if len(data) < 12 {
		return "", errors.New("short device identifier header")
	}
	size := int(binary.LittleEndian.Uint32(data[4:]))
	count := int(binary.LittleEndian.Uint32(data[8:]))
	if size < 12 || size > len(data) || count < 1 || count > 128 {
		return "", errors.New("invalid device identifier header")
	}
	data = data[:size]
	pos := 12
	var ids []string
	for i := 0; i < count; i++ {
		if pos > len(data)-16 {
			return "", errors.New("truncated device identifier")
		}
		d := data[pos:]
		length := int(binary.LittleEndian.Uint16(d[8:]))
		next := int(binary.LittleEndian.Uint16(d[10:]))
		association := binary.LittleEndian.Uint32(d[12:])
		if length == 0 || length > len(d)-16 {
			return "", errors.New("invalid device identifier size")
		}
		if association == 0 {
			ids = append(ids, fmt.Sprintf("%d:%d:%x", binary.LittleEndian.Uint32(d), binary.LittleEndian.Uint32(d[4:]), d[16:16+length]))
		}
		if i+1 < count {
			if next < 16+length || next > len(d)-16 {
				return "", errors.New("invalid next device identifier")
			}
			pos += next
		}
	}
	if len(ids) == 0 {
		return "", errors.New("no device-associated identifiers")
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte("vpd-v1\x00" + strings.Join(ids, "\x00")))
	return "v1:" + hex.EncodeToString(sum[:]), nil
}
func storagePathID(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return "", err
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
	if id != expected.ID {
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
