package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// StorageDevice is evidence, never an authority to change a binding.
// Path and drive letters deliberately do not participate in identity.
type StorageDevice struct {
	ID           string             `json:"id"`
	DiskID       string             `json:"disk_id"`
	Path         string             `json:"path"`
	SizeBytes    int64              `json:"size_bytes"`
	Boot         bool               `json:"boot"`
	BootVerified bool               `json:"boot_verified"`
	Partitions   []StoragePartition `json:"partitions"`
	// IdentityStrength says what ID is worth (docs/V4-BETA-FIXES.md §5):
	// IdentityStrong when the device reports a hardware identifier of its own
	// (an NAA or EUI-64 designator or a serial), IdentityWeak when it reports
	// none, or only the slot it sits in, so its ID is derived and the disk is
	// found by its GUID, size and layout instead. Empty only for a device with
	// no GUID either (unapprovable) and in bindings approved before this field
	// existed. omitempty keeps the observation of an unchanged older report
	// byte-identical.
	IdentityStrength string `json:"identity_strength,omitempty"`
	// Model is the disk's friendly name, for display only. It never decides a
	// binding.
	Model string `json:"model,omitempty"`
}

// Identity strengths. See StorageDevice.IdentityStrength.
const (
	IdentityStrong = "strong"
	IdentityWeak   = "weak"
)

type StoragePartition struct {
	ID     string `json:"id"`
	Offset uint64 `json:"offset"`
	Size   uint64 `json:"size"`
	Boot   bool   `json:"boot"`
	System bool   `json:"system"`
}
type StorageBinding struct {
	Target string        `json:"target"` // boot | device:<ID>
	Device StorageDevice `json:"device"`
}
type StorageApproval struct {
	Revision    int64            `json:"revision"`
	Observation string           `json:"observation"`
	Bindings    []StorageBinding `json:"bindings"`
}
type StorageStatus struct {
	Authority   string           `json:"authority,omitempty"`
	Observation string           `json:"observation"`
	Revision    int64            `json:"revision"`
	Error       string           `json:"error,omitempty"`
	Devices     []StorageDevice  `json:"devices"`
	Bindings    []StorageBinding `json:"bindings"`
}

// StorageObservation binds an approval to fresh identity evidence. Open paths
// are excluded: enumeration order is not a new device.
func StorageObservation(devices []StorageDevice) string {
	copyDevices := append([]StorageDevice(nil), devices...)
	for i := range copyDevices {
		copyDevices[i].Path = ""
		copyDevices[i].Partitions = append([]StoragePartition(nil), copyDevices[i].Partitions...)
		sort.Slice(copyDevices[i].Partitions, func(a, b int) bool { return copyDevices[i].Partitions[a].Offset < copyDevices[i].Partitions[b].Offset })
	}
	sort.Slice(copyDevices, func(i, j int) bool { return copyDevices[i].ID < copyDevices[j].ID })
	b, _ := json.Marshal(copyDevices)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ResolveStorageBinding refuses unknown, ambiguous, replaced or repartitioned
// media. It never modifies the operator-approved baseline.
//
// How the approved device is found depends on the strength recorded at
// approval: a strong identity must match by ID; a weak one has no hardware
// identifier of its own, so it is found by its disk GUID instead and must be
// the only device carrying it (a copy attached beside its original is
// ambiguous, never a guess). Size and partition evidence must match either
// way. A hardware serial that appeared or disappeared since approval is an
// ordinary identifier change: it needs a new approval, and the new
// identifiers can be approved like any others.
// A binding approved before strengths existed ("") resolves exactly as it did
// then: by ID, with no strength check; approving again records the strength.
func ResolveStorageBinding(binding StorageBinding, devices []StorageDevice) (StorageDevice, error) {
	var zero StorageDevice
	if err := validateStorageDevice(binding.Device); err != nil {
		return zero, err
	}
	approved := binding.Device.IdentityStrength
	var matches []StorageDevice
	for _, d := range devices {
		if approved == IdentityWeak {
			if d.DiskID == binding.Device.DiskID {
				matches = append(matches, d)
			}
		} else if d.ID == binding.Device.ID {
			matches = append(matches, d)
		}
	}
	if len(matches) != 1 {
		if len(matches) == 0 && approved == IdentityStrong {
			// The serial may be gone: the same disk, now identified by its
			// layout. Say that, rather than "missing".
			var same []StorageDevice
			for _, d := range devices {
				if d.DiskID == binding.Device.DiskID && d.SizeBytes == binding.Device.SizeBytes {
					same = append(same, d)
				}
			}
			if len(same) == 1 {
				return zero, serialChangedError(binding.Target, approved, same[0].IdentityStrength)
			}
		}
		return zero, fmt.Errorf("storage identity missing or ambiguous for target %s", binding.Target)
	}
	current := matches[0]
	if approved != "" && current.IdentityStrength != approved {
		return zero, serialChangedError(binding.Target, approved, current.IdentityStrength)
	}
	if current.Path == "" || current.DiskID != binding.Device.DiskID || current.SizeBytes != binding.Device.SizeBytes {
		return zero, fmt.Errorf("storage disk identity changed for target %s", binding.Target)
	}
	if binding.Target == "boot" {
		bootCount := 0
		for _, d := range devices {
			if d.Boot {
				bootCount++
			}
		}
		if bootCount != 1 || !current.Boot || !current.BootVerified {
			return zero, errors.New("Boot Drive does not match verified Windows partition evidence")
		}
	} else if binding.Target != "device:"+binding.Device.ID {
		// The target names the APPROVED id: a weak device found in another
		// slot has a different current ID and is still that target.
		return zero, errors.New("invalid storage target")
	}
	if partitionEvidence(binding.Device.Partitions) != partitionEvidence(current.Partitions) {
		return zero, fmt.Errorf("storage partition identity changed for target %s", binding.Target)
	}
	return current, nil
}

// serialChangedError is the refusal for a disk whose hardware serial appeared
// or disappeared since approval. It names the change in plain words.
func serialChangedError(target, approved, now string) error {
	word := func(strength string) string {
		if strength == IdentityStrong {
			return "reported a hardware serial"
		}
		return "reported no hardware serial"
	}
	return fmt.Errorf("storage identity changed for target %s (it %s when approved, now %s); approve the storage again", target, word(approved), word(now))
}
func partitionEvidence(parts []StoragePartition) string {
	cp := append([]StoragePartition(nil), parts...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Offset < cp[j].Offset })
	b, _ := json.Marshal(cp)
	return string(b)
}

// ValidateStorageApproval is shared by local and joined resolution paths.
// Authority is checked by the caller before this data-level validation.
func ValidateStorageApproval(approval StorageApproval, status StorageStatus) error {
	if approval.Revision <= status.Revision {
		return errors.New("storage approval is stale")
	}
	if approval.Observation == "" || approval.Observation != StorageObservation(status.Devices) {
		return errors.New("storage observation changed; review current devices")
	}
	if len(approval.Bindings) == 0 {
		return errors.New("select at least one storage target")
	}
	seen := map[string]bool{}
	for _, b := range approval.Bindings {
		if seen[b.Target] {
			return errors.New("duplicate storage target")
		}
		seen[b.Target] = true
		if strings.TrimSpace(b.Target) != b.Target {
			return errors.New("invalid storage target")
		}
		if _, err := ResolveStorageBinding(b, status.Devices); err != nil {
			return err
		}
	}
	return nil
}

// Missing, overlapping or repeated partition evidence is not approvable.
func validateStorageDevice(d StorageDevice) error {
	if !strings.HasPrefix(d.ID, "v1:") || len(d.ID) != 67 || d.DiskID == "" || d.SizeBytes <= 0 || len(d.Partitions) == 0 || len(d.Partitions) > 128 {
		return errors.New("storage target requires complete device and partition evidence")
	}
	if _, err := hex.DecodeString(d.ID[3:]); err != nil {
		return errors.New("invalid storage device identity")
	}
	if d.IdentityStrength != "" && d.IdentityStrength != IdentityStrong && d.IdentityStrength != IdentityWeak {
		return errors.New("invalid storage identity strength")
	}
	parts := append([]StoragePartition(nil), d.Partitions...)
	sort.Slice(parts, func(i, j int) bool { return parts[i].Offset < parts[j].Offset })
	seen := map[string]bool{}
	var end uint64
	for _, p := range parts {
		if p.ID == "" || seen[p.ID] || p.Size == 0 || p.Offset < end || p.Offset > uint64(d.SizeBytes) || p.Size > uint64(d.SizeBytes)-p.Offset {
			return errors.New("storage partition evidence is missing, duplicated or outside the disk")
		}
		seen[p.ID] = true
		end = p.Offset + p.Size
	}
	return nil
}
