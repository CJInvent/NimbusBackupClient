//go:build windows
// +build windows

package main

import (
	"context"
	"controlplane"
	"errors"
	"fmt"
	"io"
	"os"
	"pbscommon"
	"regexp"
	"snapshot"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"crypto/sha256"
	"encoding/hex"
	"math"
	"slices"
	"sync"

	"golang.org/x/sys/windows"
)

type DISK_EXTENT struct {
	DiskNumber     uint32
	StartingOffset int64
	ExtentLength   int64
}

type VOLUME_DISK_EXTENTS struct {
	NumberOfDiskExtents uint32
	Extents             [16]DISK_EXTENT
}

type PARTITION_STYLE uint32

const (
	PartitionStyleMBR PARTITION_STYLE = 0
	PartitionStyleGPT PARTITION_STYLE = 1
)

type PARTITION_INFORMATION_EX struct {
	PartitionStyle     PARTITION_STYLE
	Partitionordinal   uint16
	StartingOffset     uint64
	PartitionLength    uint64
	PartitionNumber    uint32
	RewritePartition   bool
	IsServicePartition bool
	Padding            [112]byte
}

type GET_LENGTH_INFORMATION struct {
	Length int64
}

type DRIVE_LAYOUT_INFORMATION_EX struct {
	PartitionStyle uint32
	PartitionCount uint32
	PlaceHolder    [36]byte
	PartitionEntry [128]PARTITION_INFORMATION_EX
}

const IOCTL_DISK_GET_DRIVE_LAYOUT_EX = 0x00070050
const IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS = 0x00560000
const IOCTL_DISK_GET_LENGTH_INFO = 0x0007405C

// FSCTL_ALLOW_EXTENDED_DASD_IO lifts the file system's bound on volume-handle
// reads. See allowExtendedDASD.
const FSCTL_ALLOW_EXTENDED_DASD_IO = 0x00090083

// IOCTL_DISK_GET_DRIVE_GEOMETRY_EX is defined with FILE_ANY_ACCESS, so it
// works on a device handle opened with dwDesiredAccess == 0 (no elevation),
// unlike IOCTL_DISK_GET_LENGTH_INFO which requires FILE_READ_ACCESS and thus
// admin rights on a raw \\.\PhysicalDrive. Used to size disks for the picker
// without requiring the GUI to run elevated.
const IOCTL_DISK_GET_DRIVE_GEOMETRY_EX = 0x000700A0

// diskGeometryEx is the fixed prefix of DISK_GEOMETRY_EX. The real structure
// has a variable-length data tail (partition + detection info); we
// over-allocate the output buffer and read only this prefix. DiskSize is the
// total capacity in bytes (LARGE_INTEGER at offset 24).
type diskGeometryEx struct {
	Cylinders         int64  // LARGE_INTEGER
	MediaType         uint32 // MEDIA_TYPE enum
	TracksPerCylinder uint32
	SectorsPerTrack   uint32
	BytesPerSector    uint32
	DiskSize          int64 // LARGE_INTEGER
}

var (
	modkernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	procFindFirstVolumeW             = modkernel32.NewProc("FindFirstVolumeW")
	procFindNextVolumeW              = modkernel32.NewProc("FindNextVolumeW")
	procFindVolumeClose              = modkernel32.NewProc("FindVolumeClose")
	procGetVolumePathNamesForVolumeW = modkernel32.NewProc("GetVolumePathNamesForVolumeNameW")
)

type VolumeLetterAssign struct {
	DiskNumber int32
	Offset     uint64
	VolumePath string
	Letters    []string
}

type Partition struct {
	StartByte   uint64
	EndByte     uint64
	RequiresVSS bool
	Skip        bool
	VolumePath  string
}

// PhysicalDiskInfo contains information about a physical disk
type PhysicalDiskInfo struct {
	Identity   string   `json:"identity"`
	Target     string   `json:"target"`
	DiskNumber int      `json:"diskNumber"`
	SizeBytes  int64    `json:"sizeBytes"`
	SizeText   string   `json:"sizeText"`
	Letters    []string `json:"letters"`
	Label      string   `json:"label"`
	Path       string   `json:"path"`
}

func enumVolumeDiskOffset() ([]VolumeLetterAssign, error) {
	ret := make([]VolumeLetterAssign, 0)
	volumeName := make([]uint16, windows.MAX_PATH)

	r1, _, _ := procFindFirstVolumeW.Call(
		uintptr(unsafe.Pointer(&volumeName[0])),
		uintptr(len(volumeName)),
	)
	if r1 == 0 {
		return ret, nil
	}
	findHandle := windows.Handle(r1)
	defer procFindVolumeClose.Call(uintptr(findHandle))

	for {
		volName := windows.UTF16ToString(volumeName)

		hVol, err := windows.CreateFile(
			windows.StringToUTF16Ptr(volName[:len(volName)-1]),
			0, // metadata-only; IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS is FILE_ANY_ACCESS
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil,
			windows.OPEN_EXISTING,
			0,
			0,
		)
		if err == nil {
			buffer := make([]byte, 1024)
			buffer2 := make([]uint16, 1024)
			var bytesReturned uint32

			err := windows.DeviceIoControl(
				hVol,
				IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS,
				nil,
				0,
				&buffer[0],
				uint32(len(buffer)),
				&bytesReturned,
				nil,
			)
			if err == nil {
				extents := (*VOLUME_DISK_EXTENTS)(unsafe.Pointer(&buffer[0]))

				for i := uint32(0); i < extents.NumberOfDiskExtents; i++ {
					var returnLength uint32
					extent := (*DISK_EXTENT)(unsafe.Pointer(
						uintptr(unsafe.Pointer(&extents.Extents[0])) +
							uintptr(i)*unsafe.Sizeof(DISK_EXTENT{}),
					))

					v := VolumeLetterAssign{
						DiskNumber: int32(extent.DiskNumber),
						Offset:     uint64(extent.StartingOffset),
						VolumePath: volName,
						Letters:    make([]string, 0),
					}

					r1, _, _ := procGetVolumePathNamesForVolumeW.Call(
						uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(volName))),
						uintptr(unsafe.Pointer(&buffer2[0])),
						uintptr(len(buffer2)),
						uintptr(unsafe.Pointer(&returnLength)),
					)

					if r1 == 0 && returnLength > uint32(len(buffer2)) {
						buffer2 = make([]uint16, returnLength)
						r1, _, _ = procGetVolumePathNamesForVolumeW.Call(
							uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(volName))),
							uintptr(unsafe.Pointer(&buffer2[0])), uintptr(len(buffer2)),
							uintptr(unsafe.Pointer(&returnLength)),
						)
					}
					if r1 == 0 {
						writeWarnLog(fmt.Sprintf("Could not enumerate mount paths for disk %d", v.DiskNumber))
					} else {
						v.Letters = volumeMountPaths(buffer2)
					}

					ret = append(ret, v)
				}
			}

			windows.CloseHandle(hVol)
		}

		ret, _, _ := procFindNextVolumeW.Call(
			uintptr(findHandle),
			uintptr(unsafe.Pointer(&volumeName[0])),
			uintptr(len(volumeName)),
		)
		if ret == 0 {
			break
		}
	}
	return ret, nil
}

// allowExtendedDASD lets reads on a volume handle reach the end of the
// device instead of stopping at the end of the file system on it. NTFS sizes
// itself one sector short of its partition (the last sector holds the backup
// boot sector), and without this flag it cuts off any volume-handle read that
// crosses its own end. IOCTL_DISK_GET_LENGTH_INFO still reports the full
// device length, so the image reader asks for bytes the handle then refuses:
// a deterministic short read on the final block of every NTFS snapshot.
func allowExtendedDASD(f *os.File) error {
	var bytesReturned uint32
	return windows.DeviceIoControl(windows.Handle(f.Fd()), FSCTL_ALLOW_EXTENDED_DASD_IO,
		nil, 0, nil, 0, &bytesReturned, nil)
}

func GetDiskLength(path string) (int64, error) {
	// Two-stage query. IOCTL_DISK_GET_LENGTH_INFO works on both disks and
	// volume devices (including VSS shadow-copy volumes) but carries
	// FILE_READ_ACCESS, so it needs a GENERIC_READ handle (privileged for raw
	// \\.\PhysicalDrive). IOCTL_DISK_GET_DRIVE_GEOMETRY_EX is FILE_ANY_ACCESS
	// (works on a zero-access handle for non-admin enumeration) but is a
	// disk-class IOCTL and fails with ERROR_INVALID_FUNCTION ("Incorrect
	// function") on volume devices like snapshots. Try length-info first, fall
	// back to geometry, so both the privileged backup path (snapshot volumes)
	// and the unprivileged GUI picker (raw disks) get a size.
	if l, err := getDiskLengthViaLengthInfo(path); err == nil {
		return l, nil
	}
	return getDiskLengthViaGeometry(path)
}

func getDiskLengthViaLengthInfo(path string) (int64, error) {
	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return 0, fmt.Errorf("createFile failed: %w", err)
	}
	defer windows.CloseHandle(handle)

	var lengthInfo GET_LENGTH_INFORMATION
	var bytesReturned uint32
	err = windows.DeviceIoControl(
		handle,
		IOCTL_DISK_GET_LENGTH_INFO,
		nil,
		0,
		(*byte)(unsafe.Pointer(&lengthInfo)),
		uint32(unsafe.Sizeof(lengthInfo)),
		&bytesReturned,
		nil,
	)
	if err != nil {
		return 0, fmt.Errorf("deviceIoControl failed: %w", err)
	}
	return lengthInfo.Length, nil
}

func getDiskLengthViaGeometry(path string) (int64, error) {
	// Open with dwDesiredAccess == 0: enough to query device metadata, and
	// permitted for non-elevated callers (GENERIC_READ on a raw PhysicalDrive
	// requires admin). The actual backup read path still uses GENERIC_READ.
	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return 0, fmt.Errorf("createFile failed: %w", err)
	}
	defer windows.CloseHandle(handle)

	// DISK_GEOMETRY_EX has a variable-length tail; over-allocate the buffer so
	// DeviceIoControl doesn't fail with ERROR_INSUFFICIENT_BUFFER.
	buf := make([]byte, 4096)
	var bytesReturned uint32
	err = windows.DeviceIoControl(
		handle,
		IOCTL_DISK_GET_DRIVE_GEOMETRY_EX,
		nil,
		0,
		&buf[0],
		uint32(len(buf)),
		&bytesReturned,
		nil,
	)
	if err != nil {
		return 0, fmt.Errorf("deviceIoControl failed: %w", err)
	}
	if bytesReturned < uint32(unsafe.Sizeof(diskGeometryEx{})) {
		return 0, fmt.Errorf("unexpected geometry size: %d bytes", bytesReturned)
	}

	geo := (*diskGeometryEx)(unsafe.Pointer(&buf[0]))
	return geo.DiskSize, nil
}

func BytesToString(b int64) string {
	if b < 1024 {
		return fmt.Sprintf("%dB", b)
	}
	if b < 1024*1024 {
		return fmt.Sprintf("%dKB", b/1024)
	}
	if b < 1024*1024*1024 {
		return fmt.Sprintf("%dMB", b/(1024*1024))
	}
	return fmt.Sprintf("%dGB", b/(1024*1024*1024))
}

// ListPhysicalDisks returns a list of available physical disks with their information
func ListPhysicalDisks() ([]PhysicalDiskInfo, error) {
	writeDebugLog("Enumerating physical disks...")

	// Get volume to disk mapping
	vols, err := enumVolumeDiskOffset()
	if err != nil {
		return nil, fmt.Errorf("Failed to enumerate volumes: %v", err)
	}

	// Group letters by disk number
	diskLetters := make(map[int][]string)
	for _, v := range vols {
		diskNum := int(v.DiskNumber)
		for _, letter := range v.Letters {
			// Extract just the drive letter (e.g., "C:" from "C:\")
			letter = strings.TrimRight(letter, "\\")
			if !contains(diskLetters[diskNum], letter) {
				diskLetters[diskNum] = append(diskLetters[diskNum], letter)
			}
		}
	}

	// Try to enumerate disks 0-9
	disks := make([]PhysicalDiskInfo, 0)
	for i := 0; i < 10; i++ {
		diskPath := fmt.Sprintf("\\\\.\\PhysicalDrive%d", i)
		size, err := GetDiskLength(diskPath)
		if err != nil {
			// Disk doesn't exist or can't be accessed
			continue
		}

		letters := diskLetters[i]
		if letters == nil {
			letters = []string{}
		}
		sort.Strings(letters)

		// Build label
		label := fmt.Sprintf("Disk %d", i)
		if len(letters) > 0 {
			label += fmt.Sprintf(" (%s)", strings.Join(letters, ", "))
		}
		label += fmt.Sprintf(" - %s", BytesToString(size))

		disks = append(disks, PhysicalDiskInfo{
			DiskNumber: i,
			SizeBytes:  size,
			SizeText:   BytesToString(size),
			Letters:    letters,
			Label:      label,
			Path:       diskPath,
		})

		writeDebugLog(fmt.Sprintf("Found: %s", label))
	}

	devices, err := discoverStorageDevices()
	if err != nil {
		return nil, err
	}
	for i := range disks {
		for _, device := range devices {
			if device.Path == disks[i].Path {
				disks[i].Identity = device.ID
				disks[i].Target = "device:" + device.ID
				if device.Boot {
					disks[i].Target = "boot"
				}
			}
		}
	}
	return disks, nil
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

type MachineChunkState struct {
	assignments       []string
	indexHashData     map[uint64][]byte
	assignmentsOffset []uint64
	processedSize     uint64
	wrid              uint64
	chunkcount        uint64
	currentChunk      []byte
	newchunk          *atomic.Uint64
	reusechunk        *atomic.Uint64
	knownChunks       *pbscommon.ChunkSet
}

func (c *MachineChunkState) Init(newchunk *atomic.Uint64, reusechunk *atomic.Uint64, knownChunks *pbscommon.ChunkSet) {
	c.assignments = make([]string, 0)
	c.assignmentsOffset = make([]uint64, 0)
	c.processedSize = 0
	c.chunkcount = 0
	c.indexHashData = make(map[uint64][]byte)
	c.currentChunk = make([]byte, 0)
	c.reusechunk = reusechunk
	c.newchunk = newchunk
	c.knownChunks = knownChunks
}

// machineStatsFn, when non-nil, receives structured live stats from the chunk
// uploader (bytesDone, bytesTotal, newChunks, reusedChunks). Set by
// RunMachineBackup while holding the per-destination backup lock, which also
// guarantees a single machine backup at a time, so a package var is safe here.
var machineStatsFn func(bytesDone, bytesTotal, newChunks, reusedChunks uint64)

// chunkCounters accumulates chunk accounting for ONE machine backup, across
// every disk in it.
//
// These used to be allocated INSIDE uploadWorker, which is per-archive: a
// second disk restarted the live counts from zero, and the run-level totals
// did not exist anywhere at all -- which is why RunMachineBackup reported
// new/reused as zero "rather than guessed". Owning them at the run makes the
// number real, and makes the dedup story on the run row (docs/V4-UX.md §6)
// the whole machine rather than its last disk.
type chunkCounters struct {
	newChunks    atomic.Uint64
	reusedChunks atomic.Uint64
}

func uploadWorker(client *pbscommon.PBSClient, counters *chunkCounters, filename string, totalSize uint64, ch chan []byte, readerErr <-chan error, progress func(float64, string)) error {
	newchunk := &counters.newChunks
	reusechunk := &counters.reusedChunks
	knownChunks := pbscommon.NewChunkSet()

	// abort unblocks the disk reader before returning an error: the reader
	// goroutine may be parked on a channel send, and while parked it pins the
	// raw-disk and VSS-snapshot handles for the life of the process. Draining
	// its channel (and result slot) in the background lets it run to
	// completion and release everything.
	abort := func(e error) error {
		go func() {
			for range ch {
			}
			select {
			case <-readerErr:
			default:
			}
		}()
		return e
	}

	knownChunks2, err := client.GetKnownSha265FromFIDX(filename)
	if err == nil {
		knownChunks = knownChunks2
		writeDebugLog(fmt.Sprintf("Loaded %d known chunks from previous backup", knownChunks.Len()))
	} else {
		// This is the session's FIRST request, so a handshake rejection (bad
		// namespace, permissions, locked backup group, ...) surfaces here. It
		// must abort: continuing would hit CreateFixedIndex, whose re-dial is
		// blocked by design and reports a misleading "session cannot be
		// resumed" error that masks the real cause.
		var authErr *pbscommon.PBSResponseError
		if errors.As(err, &authErr) || strings.Contains(err.Error(), "PBS authentication failed") {
			writeErrorLog(fmt.Sprintf("PBS rejected the backup session: %v", err))
			return abort(fmt.Errorf("PBS rejected the backup session: %w", err))
		}
		// Anything else just means no usable previous index (normal for a
		// first backup): start with an empty known-chunk set.
		writeWarnLog(fmt.Sprintf("No previous backup found: %v", err))
	}

	CS := MachineChunkState{}
	CS.Init(newchunk, reusechunk, knownChunks)
	wrid, err := client.CreateFixedIndex(pbscommon.FixedIndexCreateReq{
		ArchiveName: filename,
		Size:        int64(totalSize),
	})
	if err != nil {
		return abort(err)
	}

	var assignmentMutex sync.Mutex

	errch := make(chan error, 8)
	stop := make(chan struct{})
	var stopOnce sync.Once
	digests := make(map[int64][]byte)

	type PosSeg struct {
		Pos  uint64
		Data []byte
	}

	ch2 := make(chan PosSeg, 8)

	workerfn := func() {
		var workerErr error
		defer func() { errch <- workerErr }()
		for {
			var seg PosSeg
			select {
			case <-stop:
				return
			case next, ok := <-ch2:
				if !ok {
					return
				}
				seg = next
			}
			digest := client.ChunkDigest(seg.Data)
			shahash := hex.EncodeToString(digest[:])

			assignmentMutex.Lock()
			CS.indexHashData[seg.Pos] = digest[:]
			digests[int64(seg.Pos)] = digest[:]

			_, exists := knownChunks.GetOrInsert(shahash, true)
			assignmentMutex.Unlock()

			if exists {
				reusechunk.Add(1)
			} else {
				uploadErr := client.UploadFixedCompressedChunk(wrid, shahash, seg.Data)
				if uploadErr != nil {
					workerErr = uploadErr
					return
				}
				newchunk.Add(1)
			}

			assignmentMutex.Lock()
			CS.assignments = append(CS.assignments, shahash)
			CS.assignmentsOffset = append(CS.assignmentsOffset, seg.Pos)
			CS.processedSize += uint64(len(seg.Data))
			CS.chunkcount++
			processedSnapshot := CS.processedSize
			chunkcountSnapshot := CS.chunkcount
			assignmentMutex.Unlock()

			// Progress/stats callbacks and error checks happen OUTSIDE the
			// mutex: holding it through the callback serialized all 8 workers
			// on every chunk, and the old over-size error path broke out of
			// the loop while still holding the lock, deadlocking the others.
			percent := float64(processedSnapshot) / float64(totalSize)
			// Counters travel on the structured stats channel ONLY — the UI
			// renders them once, in the stats grid. Baking them into the
			// message too is what put chunk counts on screen twice.
			if progress != nil {
				progress(0.1+percent*0.85, "")
			}
			if machineStatsFn != nil {
				machineStatsFn(processedSnapshot, totalSize, newchunk.Load(), reusechunk.Load())
			}
			// Chunk-cadence diagnostics: one line per 256 chunks (~1 GB).
			if chunkcountSnapshot%256 == 0 {
				totalChunks := int(math.Ceil(float64(totalSize) / float64(pbscommon.PBS_FIXED_CHUNK_SIZE)))
				writeCatLog(catChunks, fmt.Sprintf("chunk %d/%d uploaded=%d reused=%d",
					chunkcountSnapshot, totalChunks, newchunk.Load(), reusechunk.Load()))
			}

			if processedSnapshot > totalSize {
				workerErr = fmt.Errorf("fatal: tried to backup more data than specified size")
				return
			}
		}
	}

	posfn := func() {
		defer close(ch2)
		pos := uint64(0)
		for block := range ch {
			select {
			case ch2 <- PosSeg{Pos: pos, Data: block}:
			case <-stop:
				return
			}
			pos += uint64(len(block))
		}
	}

	go posfn()

	for i := 0; i < 8; i++ {
		go workerfn()
	}
	var firstUploadErr error
	for i := 0; i < 8; i++ {
		if workerErr := <-errch; workerErr != nil {
			if firstUploadErr == nil {
				firstUploadErr = workerErr
			}
			stopOnce.Do(func() { close(stop) })
		}
	}
	if firstUploadErr != nil {
		return abort(firstUploadErr)
	}

	// The stream is fully drained; now learn how the reader ended. A read
	// failure means the index is incomplete — abort instead of closing it with
	// a mismatched chunk count (or worse, a silently truncated image).
	if rerr := <-readerErr; rerr != nil {
		return fmt.Errorf("disk read failed, aborting index close: %w", rerr)
	}

	// Assign chunks
	for k := 0; k < len(CS.assignments); k += 128 {
		k2 := k + 128
		if k2 > len(CS.assignments) {
			k2 = len(CS.assignments)
		}
		err = client.AssignFixedChunks(wrid, CS.assignments[k:k2], CS.assignmentsOffset[k:k2])
		if err != nil {
			return err
		}
	}

	chunkdigests := sha256.New()
	positions := make([]uint64, 0, len(CS.indexHashData))
	for k := range CS.indexHashData {
		positions = append(positions, k)
	}
	slices.Sort(positions)
	for _, P := range positions {
		_, _ = chunkdigests.Write(CS.indexHashData[P])
	}

	err = client.CloseFixedIndex(wrid, hex.EncodeToString(chunkdigests.Sum(nil)), CS.processedSize, CS.chunkcount)
	if err != nil {
		return err
	}
	return nil
}

// onPhase carries the run's lifecycle to the control plane. The machine
// engine never called it: only the directory engine did, so an image backup
// sat at "preparing" in the portal for its entire duration and then jumped
// straight to success. The scheduled proof run on 2026-09-15 spent three and
// a half minutes moving 80 GB while every page showed it preparing.
func backupWindowsDisk(ctx context.Context, client *pbscommon.PBSClient, counters *chunkCounters, index int, progress func(float64, string), onPhase func(string), onMilestone func(checkpoint, level, message string), validateSource func(uintptr, string) error) (int64, error) {
	writeDebugLog(fmt.Sprintf("Starting backup of PhysicalDrive%d", index))

	parts := make([]Partition, 0)
	// 32 x 4 MB = 128 MB of elastic buffer between the disk reader and the
	// hash/compress/upload workers. The old depth of 8 made read speed
	// mirror every upload hiccup (the observed 60<->400 MB/s sawtooth):
	// the reader parked the instant the pipeline paused, then burst.
	ch := make(chan []byte, 32)
	diskdev := fmt.Sprintf("\\\\.\\PhysicalDrive%d", index)

	volumeHandle, err := syscall.CreateFile(
		syscall.StringToUTF16Ptr(diskdev),
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil,
		syscall.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return 0, fmt.Errorf("Failed to open %s: %v", diskdev, err)
	}
	defer syscall.CloseHandle(volumeHandle)
	if validateSource == nil {
		return 0, errors.New("image source identity validator missing")
	}
	if err := validateSource(uintptr(volumeHandle), diskdev); err != nil {
		return 0, err
	}

	var volumeDiskExtents DRIVE_LAYOUT_INFORMATION_EX
	var bytesReturned uint32

	err = syscall.DeviceIoControl(
		volumeHandle,
		IOCTL_DISK_GET_DRIVE_LAYOUT_EX,
		nil,
		0,
		(*byte)(unsafe.Pointer(&volumeDiskExtents)),
		uint32(unsafe.Sizeof(volumeDiskExtents)),
		&bytesReturned,
		nil,
	)

	if err != nil {
		return 0, fmt.Errorf("Failed to get drive layout: %v", err)
	}

	vols, err := enumVolumeDiskOffset()
	if err != nil {
		return 0, fmt.Errorf("Failed to enumerate volumes: %v", err)
	}

	for i := 0; i < int(volumeDiskExtents.PartitionCount); i++ {
		E := volumeDiskExtents.PartitionEntry[i]
		if E.PartitionNumber == 0 {
			continue
		}
		writeDebugLog(fmt.Sprintf("Partition %d: offset=%s, length=%s",
			E.PartitionNumber, BytesToString(int64(E.StartingOffset)), BytesToString(int64(E.PartitionLength))))
		if onMilestone != nil {
			onMilestone(controlplane.CheckpointDisksPartitions, "info", fmt.Sprintf(
				"PhysicalDrive%d, partition %d: offset=%s, length=%s",
				index, E.PartitionNumber, BytesToString(int64(E.StartingOffset)), BytesToString(int64(E.PartitionLength))))
		}

		var volumePath string
		for _, V := range vols {
			if V.DiskNumber == int32(index) && V.Offset == E.StartingOffset {
				if len(V.Letters) > 0 {
					volumePath = V.VolumePath
				}
			}
		}

		parts = append(parts, Partition{
			StartByte:   uint64(E.StartingOffset),
			EndByte:     uint64(E.StartingOffset + E.PartitionLength),
			RequiresVSS: volumePath != "",
			Skip:        false,
			VolumePath:  volumePath,
		})
	}

	snapshotPaths := imageSnapshotPaths(parts)

	total, err := GetDiskLength(diskdev)
	if err != nil {
		return 0, err
	}

	writeDebugLog(fmt.Sprintf("Total disk size: %s", BytesToString(total)))

	if onMilestone != nil && len(snapshotPaths) > 0 {
		onMilestone(controlplane.CheckpointSnapshotVSS, "info", fmt.Sprintf(
			"Requesting VSS snapshot for %d partition(s) on PhysicalDrive%d", len(snapshotPaths), index))
	}

	return total, snapshot.CreateVSSSnapshot(snapshotPaths, func(snapshots map[string]snapshot.SnapShot) error {
		if onMilestone != nil && len(snapshotPaths) > 0 {
			// This callback only runs once VSS has actually succeeded --
			// same product definition OnPhase's own "running" signal
			// already relies on ("for VSS jobs this fires when the shadow
			// copy EXISTS") -- so this is a genuine confirmation, not a
			// guess.
			onMilestone(controlplane.CheckpointSnapshotVSS, "info", fmt.Sprintf(
				"VSS snapshot confirmed for PhysicalDrive%d", index))
		}
		// RUNNING, by the same product definition the directory engine uses:
		// the shadow copy exists (or there was none to make) and bytes are
		// about to move. The reporter dedupes, so a second disk is free.
		if onPhase != nil {
			onPhase("running")
		}
		// Fill gaps between partitions
		newparts := make([]Partition, 0)
		var curpos uint64 = 0
		for _, P := range parts {
			if P.StartByte != curpos {
				newparts = append(newparts, Partition{
					StartByte:   curpos,
					EndByte:     P.StartByte,
					RequiresVSS: false,
					VolumePath:  "",
					Skip:        false,
				})
			}
			newparts = append(newparts, P)
			curpos = P.EndByte
		}
		if curpos < uint64(total) {
			newparts = append(newparts, Partition{
				StartByte:   curpos,
				EndByte:     uint64(total),
				RequiresVSS: false,
				VolumePath:  "",
				Skip:        false,
			})
		}
		parts = newparts

		F, err := os.Open(diskdev)
		if err != nil {
			return fmt.Errorf("failed to open disk: %v", err)
		}
		defer F.Close()
		if err := validateSource(F.Fd(), diskdev); err != nil {
			return err
		}

		// The reader signals its outcome here (buffered so it never blocks).
		// Any read/seek/snapshot failure MUST abort the whole disk backup:
		// silently closing ch would truncate the stream and the index close
		// would be rejected by PBS with a chunk-count mismatch (and a padded
		// stream would silently corrupt the image, which is worse).
		readerErr := make(chan error, 1)
		failRead := func(err error) {
			writeErrorLog(fmt.Sprintf("Disk read failed: %v", err))
			readerErr <- err
			close(ch)
		}

		go func() {
			// Chunk assembly WITHOUT the old append/re-slice churn: `buffer =
			// append(buffer, ...)` + `buffer = buffer[CHUNK:]` re-copied every
			// byte at least once more and generated a 4-8 MB allocation per
			// chunk of GC pressure. Here each chunk is one fresh slice,
			// filled directly by disk reads, handed to the channel untouched.
			chunk := make([]byte, pbscommon.PBS_FIXED_CHUNK_SIZE)
			fill := 0
			send := func(force bool) {
				if fill == int(pbscommon.PBS_FIXED_CHUNK_SIZE) || (force && fill > 0) {
					ch <- chunk[:fill]
					chunk = make([]byte, pbscommon.PBS_FIXED_CHUNK_SIZE)
					fill = 0
				}
			}
			// feed appends read bytes into the current chunk, emitting as
			// chunks complete. src semantics: keep calling read into the
			// spare capacity so partition boundaries pack, matching the old
			// behaviour (chunks can span partitions).
			feed := func(p []byte) {
				for len(p) > 0 {
					n := copy(chunk[fill:], p)
					fill += n
					p = p[n:]
					send(false)
				}
			}

			for idx, P := range parts {
				if err := backupCancelled(ctx); err != nil {
					failRead(err)
					return
				}
				writeDebugLog(fmt.Sprintf("Processing partition %d: %s to %s",
					idx, BytesToString(int64(P.StartByte)), BytesToString(int64(P.EndByte))))

				if !P.RequiresVSS {
					_, err := F.Seek(int64(P.StartByte), io.SeekStart)
					if err != nil {
						failRead(fmt.Errorf("seek to %d failed: %w", P.StartByte, err))
						return
					}
					block := make([]byte, pbscommon.PBS_FIXED_CHUNK_SIZE)
					pos := P.StartByte
					for pos < P.EndByte {
						if err := backupCancelled(ctx); err != nil {
							failRead(err)
							return
						}
						nbytes, err := F.Read(block[:min(uint64(len(block)), P.EndByte-pos)])
						if err != nil {
							failRead(fmt.Errorf("raw read at %d failed: %w", pos, err))
							return
						}
						feed(block[:nbytes])
						pos += uint64(nbytes)
					}
					if pos != P.EndByte {
						writeErrorLog(fmt.Sprintf("Failed to read partition entirely %d/%d", pos, P.EndByte))
					}
				} else {
					snap, err := imagePartitionSnapshot(P, snapshots)
					if err != nil {
						failRead(err)
						return
					}

					snapshotFile, err := os.Open(strings.TrimRight(snap.ObjectPath, "\\"))
					if err != nil {
						failRead(fmt.Errorf("open snapshot %s failed: %w", snap.ObjectPath, err))
						return
					}
					defer snapshotFile.Close()
					if err := allowExtendedDASD(snapshotFile); err != nil {
						failRead(fmt.Errorf("enable full-device reads on snapshot %s failed: %w", snap.ObjectPath, err))
						return
					}

					pos := P.StartByte
					l, err := GetDiskLength(strings.TrimRight(snap.ObjectPath, "\\"))
					if err != nil {
						failRead(fmt.Errorf("size snapshot %s failed: %w", snap.ObjectPath, err))
						return
					}

					npad, err := snapshotPadding(P.EndByte-P.StartByte, l)
					if err != nil {
						failRead(err)
						return
					}
					if npad > 0 {
						writeWarnLog("VSS volume is smaller than the partition; padding only the declared partition tail")
					}
					block := make([]byte, pbscommon.PBS_FIXED_CHUNK_SIZE)
					remaining := uint64(l)
					for remaining > 0 {
						if err := backupCancelled(ctx); err != nil {
							failRead(err)
							return
						}
						nbytes, err := io.ReadFull(snapshotFile, block[:min(uint64(len(block)), remaining)])
						if err != nil {
							failRead(fmt.Errorf("snapshot truncated at %d: read %d of %d bytes, %d left of snapshot length %d: %w",
								pos, nbytes, min(uint64(len(block)), remaining), remaining, l, err))
							return
						}
						pos += uint64(nbytes)
						remaining -= uint64(nbytes)
						feed(block[:nbytes])
					}

					// Zero padding to the partition boundary.
					zeros := make([]byte, pbscommon.PBS_FIXED_CHUNK_SIZE)
					for npad > 0 {
						sl := zeros[:min(pbscommon.PBS_FIXED_CHUNK_SIZE, npad)]
						feed(sl)
						pos += uint64(len(sl))
						npad -= uint64(len(sl))
					}
					if pos != P.EndByte {
						writeErrorLog(fmt.Sprintf("Failed to read partition entirely %d/%d", pos, P.EndByte))
					}
				}
			}

			// Flush the final partial chunk.
			send(true)

			readerErr <- nil
			close(ch)
		}()

		return uploadWorker(client, counters, fmt.Sprintf("drive-sata%d.img.fidx", index), uint64(total), ch, readerErr, progress)
	})
}

// machineBackupFailedMsg is what the UI (history, progress) shows on failure.
// Detailed, redacted cause is preserved in the returned error and server report.
const machineBackupFailedMsg = errBackupFailedSeeLog

// RunMachineBackup performs a full physical disk backup
func RunMachineBackup(opts BackupOptions) error {
	writeDebugLog("Starting machine backup")
	startTime := time.Now()

	// Validate options
	if opts.BaseURL == "" || opts.AuthID == "" || opts.Secret == "" {
		return errors.New(errPBSParamsRequired)
	}

	if len(opts.BackupDirs) == 0 {
		return errors.New(errDiskRequired)
	}

	// Serialize backups per destination. Overlapping sessions to the same
	// backup group make PBS fail group locking ("while creating locked backup
	// group"), which is exactly what repeated one-shot clicks produced.
	lock := getBackupLock(opts.BaseURL, opts.Datastore)
	if !lock.TryLock() {
		msg := errAlreadyRunning
		writeDebugLog(msg)
		if opts.OnComplete != nil {
			opts.OnComplete(false, msg)
		}
		return errors.New(msg)
	}
	defer lock.Unlock()

	// Chunk accounting for this run, shared by every disk in it.
	counters := &chunkCounters{}

	// Keep the redacted cause in both local logs and the terminal run report.
	fail := func(detail string) error {
		detail = redactLogLine(detail)
		writeErrorLog(detail)
		writeBackupLog(detail)
		if opts.OnComplete != nil {
			opts.OnComplete(false, machineBackupFailedMsg)
		}
		return fmt.Errorf("%s :: %s", machineBackupFailedMsg, detail)
	}

	// stopped reports a user-initiated Stop. Deliberately distinct from fail:
	// a stop is not a fault, and labelling it one sends the operator to the log
	// hunting a failure that never happened. By the time this runs the reader has
	// already aborted before the index was committed (PBS discards the partial)
	// and the deferred VSS Release has removed the shadow copy and its symlink.
	stopped := func() error {
		writeErrorLog("Machine backup stopped by user — aborted before index commit; partial backup discarded and VSS snapshot released")
		if opts.OnComplete != nil {
			opts.OnComplete(false, errBackupStopped)
		}
		return errors.New(errBackupStopped)
	}

	// Create PBS client
	client := &pbscommon.PBSClient{
		BaseURL:                opts.BaseURL,
		CertFingerPrint:        opts.CertFingerprint,
		AuthID:                 opts.AuthID,
		Secret:                 opts.Secret,
		Datastore:              opts.Datastore,
		Namespace:              opts.Namespace,
		Insecure:               opts.CertFingerprint != "",
		UploadLimitBytesPerSec: int64(opts.UploadLimitMbps * 1e6 / 8),
		Manifest: pbscommon.BackupManifest{
			BackupID: opts.BackupID,
		},
	}

	// Structured live stats for the GUI (speed and ETA are derived client-side
	// from these deltas). Set while holding the backup lock (single machine
	// backup at a time), cleared on exit. Installed AFTER the client exists
	// because the wire-byte count lives on the client.
	if opts.OnStats != nil {
		machineStatsFn = func(bytesDone, bytesTotal, newChunks, reusedChunks uint64) {
			opts.OnStats(&BackupProgressStats{
				Percent:       float64(bytesDone) / float64(bytesTotal),
				BytesDone:     bytesDone,
				BytesTotal:    bytesTotal,
				BytesUploaded: client.UploadedBytes(),
				NewChunks:     newChunks,
				ReusedChunks:  reusedChunks,
			})
		}
		defer func() { machineStatsFn = nil }()
	}

	// Encryption, if the gate resolved a key. Same rule as the directory
	// engine: a failure here fails the backup rather than falling through to
	// an unencrypted run.
	if len(opts.BackupKey) > 0 {
		fp, err := client.SetCryptKey(opts.BackupKey)
		if err != nil {
			return fail(fmt.Sprintf("Enabling backup encryption failed: %v", err))
		}
		if err := client.SetEscrowBlob(opts.EscrowBlob); err != nil {
			return fail(fmt.Sprintf("Recording the backup key escrow blob failed: %v", err))
		}
		writeBackupLog(fmt.Sprintf("[Encryption] enabled, key fingerprint %s", pbscommon.FingerprintString(fp)))
	}

	// Throttle chunk-level lines in the debug log: a 931GB disk is ~240k
	// chunks and two log lines per chunk would write millions of lines. Log
	// non-chunk messages always, chunk messages only when overall progress
	// advanced by >= 0.1%. The UI callback still gets every update.
	// Engine log policy: PHASE messages log verbatim (they mark state
	// transitions worth keeping); empty per-chunk ticks log a percent line at
	// >= 1% steps. This is THE backup-engine log line — the service relay
	// deliberately does not log the same event again.
	var lastLoggedPct float64 = -1
	progress := func(pct float64, msg string) {
		switch {
		case msg != "":
			writeDebugLog(fmt.Sprintf("Backup engine: %s (%.1f%%)", msg, pct*100))
		case pct*100-lastLoggedPct >= 1:
			lastLoggedPct = pct * 100
			writeDebugLog(fmt.Sprintf("Backup engine: %.0f%% of disk processed", pct*100))
		}
		if opts.OnProgress != nil {
			opts.OnProgress(pct, msg)
		}
	}

	progress(0.05, "Connecting to PBS...")
	client.Connect(false, "vm")

	// Parse and backup each physical drive
	var totalBytes int64
	for _, dev := range opts.BackupDirs {
		if !strings.HasPrefix(dev, "\\\\.\\PhysicalDrive") {
			return fmt.Errorf("invalid physical drive path: %s", dev)
		}

		re := regexp.MustCompile(`PhysicalDrive(\d+)$`)
		matches := re.FindStringSubmatch(dev)
		if len(matches) < 2 {
			return fmt.Errorf("Failed to parse drive number from: %s", dev)
		}

		idx, err := strconv.ParseInt(matches[1], 10, 32)
		if err != nil {
			return fmt.Errorf("Invalid drive number: %v", err)
		}

		progress(0.10, fmt.Sprintf("Backing up PhysicalDrive%d...", idx))
		diskBytes, err := backupWindowsDisk(opts.Ctx, client, counters, int(idx), progress, opts.OnPhase, opts.OnMilestone, opts.ValidateSource)
		if err != nil {
			// A cancelled context means the user pressed Stop; the read abort is
			// the mechanism, not a fault.
			if backupCancelled(opts.Ctx) != nil {
				return stopped()
			}
			return fail(fmt.Sprintf("Failed to backup PhysicalDrive%d: %v", idx, err))
		}
		totalBytes += diskBytes
	}

	progress(0.95, "Finalizing backup...")

	// BEFORE the manifest — the manifest records the files it has seen, so a
	// blob written after it is absent from what /finish validates.
	if err := client.UploadEscrowBlobIfEncrypted(); err != nil {
		return fail(fmt.Sprintf("Failed to write the backup key escrow blob: %v", err))
	}

	err := client.UploadManifest()
	if err != nil {
		return fail(fmt.Sprintf("Failed to upload manifest: %v", err))
	}

	err = client.Finish()
	if err != nil {
		return fail(fmt.Sprintf("Failed to finalize backup: %v", err))
	}

	progress(1.0, "Backup completed")
	writeDebugLog("Machine backup completed successfully")

	// Report the REAL result, not the finalizer's blind-success guess
	// (attachControlPlaneHooks's returned func, in controlplane_glue.go).
	// client.Manifest.BackupID/BackupTime are the actual coordinates PBS
	// used for this session (BackupTime is set once, inside Connect(),
	// and never changes afterward) -- exactly what the server needs to
	// reconcile this run against PBS, and what lets the control plane
	// stamp the Backup Job ID onto the snapshot itself
	// (cpStampSnapshotNotes only fires from a real OnResult, never from
	// the finalizer's fallback, precisely because THAT path has no
	// trustworthy backup-time to target).
	//
	// Every number here is measured. TotalBytes is the disk-capacity figure
	// the engine's own log already reports ("Total disk size: ..."), the
	// chunk counts are the run-level counters every disk in this backup
	// added to, and BytesUploaded is what the PBS client actually put on
	// the wire. The three answer different questions and none is derived
	// from another.
	status := &BackupStatus{
		Outcome:       OutcomeVerifiedSuccess,
		BackupID:      client.Manifest.BackupID,
		BackupTime:    client.Manifest.BackupTime,
		DurationSec:   time.Since(startTime).Seconds(),
		TotalBytes:    uint64(totalBytes),
		BytesUploaded: client.UploadedBytes(),
		NewChunks:     counters.newChunks.Load(),
		ReusedChunks:  counters.reusedChunks.Load(),
		Message:       "Machine backup completed successfully",
	}

	if opts.OnComplete != nil {
		opts.OnComplete(true, status.Message)
	}
	if opts.OnResult != nil {
		opts.OnResult(status)
	}

	return nil
}
