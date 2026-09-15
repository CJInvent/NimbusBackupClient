package main

import (
	"sync"
	"time"

	"controlplane"
)

// Reporting this machine's disks to the control plane, for the portal's
// image-job target picker (NimbusControl docs/V4-JOB-TARGETS.md section 1.1).
//
// WHY THIS IS CACHED AND CHECK-IN IS NOT. Enumerating disks opens every
// \\.\PhysicalDriveN in turn to read its length. On a machine with an
// external or spun-down disk that is not free, and check-in runs every ~120s
// forever. The list changes about as often as the hardware does, so it is
// read on a much slower clock and the cached answer rides along with each
// check-in.
//
// The cache holds the LAST GOOD reading. An enumeration that fails does not
// clear it: the server's rule is that an absent list means "no reading", so
// clearing would turn one bad read into the portal forgetting a machine's
// disks. A failure just leaves the previous answer in place until one
// succeeds.

const diskInventoryTTL = 15 * time.Minute

// diskInvCache is the cache itself, with its enumerator and clock injected so
// the behaviour above (TTL, last-good retention, no retry storm on failure)
// can be tested without a Windows disk in the room.
type diskInvCache struct {
	mu   sync.Mutex
	list func() ([]PhysicalDiskInfo, error)
	now  func() time.Time
	ttl  time.Duration

	at   time.Time
	last []controlplane.InventoryDisk
}

var diskInv = &diskInvCache{
	list: ListPhysicalDisks,
	now:  time.Now,
	ttl:  diskInventoryTTL,
}

// cpDisks returns this machine's disks for the check-in inventory, or nil
// when no enumeration has ever succeeded -- which the server reads as "no
// reading from this agent", not as "this machine has no disks".
func cpDisks() []controlplane.InventoryDisk { return diskInv.get() }

func (c *diskInvCache) get() []controlplane.InventoryDisk {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.at.IsZero() && c.now().Sub(c.at) < c.ttl {
		return c.last
	}

	disks, err := c.list()
	if err != nil {
		// Keep whatever we had, and still stamp the clock. A machine where
		// this always fails (a non-Windows build, a permissions problem)
		// would otherwise pay for the attempt on every check-in for the
		// life of the process.
		c.at = c.now()
		return c.last
	}

	out := make([]controlplane.InventoryDisk, 0, len(disks))
	for _, d := range disks {
		letters := d.Letters
		if letters == nil {
			letters = []string{}
		}
		out = append(out, controlplane.InventoryDisk{
			DiskNumber: d.DiskNumber,
			SizeBytes:  d.SizeBytes,
			Letters:    letters,
		})
	}
	c.last = out
	c.at = c.now()
	return out
}
