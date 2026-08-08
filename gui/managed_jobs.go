package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"controlplane"
)

// The server-defined job set, as the client holds it.
//
// SEPARATE FILE FROM scheduled_jobs.json, and that is the whole design. The
// managed set is replaced WHOLESALE on every check-in (docs/AGENT-API.md), and
// the only safe way to do a wholesale replace is to have nothing else in the
// container being replaced. Storing both sets in one file would mean every
// check-in rewrote the user's own jobs, and one bug in the merge would delete
// work an administrator did at the console.
//
// READ-ONLY ON THE CLIENT is not a convention here, it is a property of the
// code: nothing in this file writes a job except applyManagedJobs, which takes
// its input from a check-in. There is no create, no update, no delete, and no
// GUI binding that reaches one. A managed job changes when the org changes it.
//
// WHY IT IS PERSISTED AT ALL, given it arrives every ~120s: so the scheduler
// keeps working through an outage. An agent that held the set only in memory
// would forget every managed job on a service restart and stay idle until the
// control plane answered — which is exactly when backups matter most. The file
// is a cache of the server's intent, not a second source of truth: a check-in
// always overwrites it.

var (
	managedMu   sync.RWMutex
	managedJobs []controlplane.ManagedJob
)

func getManagedJobsPath() (string, error) {
	configDir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "managed_jobs.json"), nil
}

// loadManagedJobs restores the last known set at startup, so the scheduler has
// something to run before the first check-in completes.
func loadManagedJobs() {
	path, err := getManagedJobsPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			writeErrorLog(fmt.Sprintf("[managed] could not read %s: %v", path, err))
		}
		return
	}
	var jobs []controlplane.ManagedJob
	if err := json.Unmarshal(data, &jobs); err != nil {
		// A corrupt cache is not a reason to refuse to start. The next
		// check-in replaces it wholesale anyway.
		writeDebugLog(fmt.Sprintf("[managed] discarding unreadable job cache: %v", err))
		return
	}
	managedMu.Lock()
	managedJobs = jobs
	managedMu.Unlock()
	writeDebugLog(fmt.Sprintf("[managed] restored %d job(s) from cache", len(jobs)))
}

// applyManagedJobs replaces the managed set with what the server just sent.
//
// Wholesale, never merged. A job the org deleted appears here only as an
// absence, so merging would keep running it forever with no way for the server
// to say otherwise.
//
// A nil slice is a legitimate answer meaning "this agent has no managed jobs",
// and it MUST clear the set — treating nil as "no update" would make the last
// job an org ever deleted undeletable.
func applyManagedJobs(jobs []controlplane.ManagedJob) {
	if jobs == nil {
		jobs = []controlplane.ManagedJob{}
	}

	managedMu.Lock()
	changed := !sameManagedSet(managedJobs, jobs)
	managedJobs = jobs
	managedMu.Unlock()

	if !changed {
		// Every check-in delivers the full set, so the common case is "no
		// change". Rewriting the file ~720 times a day for nothing would
		// put avoidable wear on the disk and noise in the log.
		return
	}

	writeDebugLog(fmt.Sprintf("[managed] job set changed: now %d job(s)", len(jobs)))

	path, err := getManagedJobsPath()
	if err != nil {
		return
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return
	}
	if err := atomicWriteFile(path, data, 0600); err != nil {
		writeErrorLog(fmt.Sprintf("[managed] could not cache job set: %v", err))
	}
}

// currentManagedJobs returns a copy of the set.
func currentManagedJobs() []controlplane.ManagedJob {
	managedMu.RLock()
	defer managedMu.RUnlock()
	out := make([]controlplane.ManagedJob, len(managedJobs))
	copy(out, managedJobs)
	return out
}

// sameManagedSet compares two sets by content.
//
// Order matters and that is deliberate: the server returns them in a stable
// order (org, then group, then agent, then by name), so a difference in order
// is a real difference in what was sent, not noise to normalise away.
func sameManagedSet(a, b []controlplane.ManagedJob) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameManagedJob(a[i], b[i]) {
			return false
		}
	}
	return true
}

func sameManagedJob(x, y controlplane.ManagedJob) bool {
	if x.ID != y.ID || x.Name != y.Name || x.BackupType != y.BackupType ||
		x.UseVSS != y.UseVSS || x.Compression != y.Compression ||
		x.Schedule != y.Schedule || x.Timezone != y.Timezone {
		return false
	}
	return sameStrings(x.BackupDirs, y.BackupDirs) &&
		sameStrings(x.DriveLetters, y.DriveLetters) &&
		sameStrings(x.ExcludeList, y.ExcludeList)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// toScheduledJob adapts a managed job to the shape the scheduler and the
// engine already speak.
//
// The ID is prefixed rather than reused. Managed ids are server integers and
// local ids are client-generated strings; a bare "12" could collide with a
// local job's id, and the two sets are keyed into the same runningJobs map.
// The prefix also makes a managed job identifiable ANYWHERE it turns up — in a
// log line, in job history, in the running set — without a lookup.
//
// ScheduleTime is deliberately left empty. It is the legacy HH:MM field, and a
// managed job's schedule is a calendar expression that field cannot express;
// writing a lossy approximation into it would give the local scheduler a
// second, wrong opinion about when the job runs.
func managedToScheduledJob(m controlplane.ManagedJob) ScheduledJob {
	return ScheduledJob{
		ID:           fmt.Sprintf("managed-%d", m.ID),
		Name:         m.Name,
		ScheduleTime: "",
		RunAtStartup: false,
		BackupDirs:   m.BackupDirs,
		DriveLetters: m.DriveLetters,
		BackupID:     "",
		UseVSS:       m.UseVSS,
		BackupType:   m.BackupType,
		ExcludeList:  m.ExcludeList,
		Compression:  m.Compression,
		Enabled:      true,
	}
}

// isManagedJobID reports whether an id names a server-defined job.
//
// Used by the unmanaged-backup gate: a managed job running on its schedule is
// NOT locally authored work, so `restrict_unmanaged_backups` must not block
// it. Without this the gate would key on "the local scheduler started it" and
// silently stop the org's OWN jobs on every machine the org restricted --
// turning a lockdown into an outage.
func isManagedJobID(id string) bool {
	return strings.HasPrefix(id, "managed-")
}
