//go:build service
// +build service

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"controlplane"
)

// Firing managed jobs on their calendar schedules.
//
// The last piece of V4-CLIENT-CONFIG.md §2/§3: the server has been able to
// DEFINE a schedule since the job model landed, and the agent has been able to
// receive it, but nothing evaluated it. Managed jobs could only be triggered
// from the portal.
//
// Service-only, like everything else that runs a backup.
//
// ---------------------------------------------------------------- state
//
// A THIRD file, and the split is not arbitrary:
//
//	scheduled_jobs.json      the user's own jobs        — user-owned
//	managed_jobs.json        the server's job set       — server-owned, replaced wholesale
//	managed_job_state.json   when each last fired       — agent-owned
//
// The state MUST NOT live in managed_jobs.json, because that file is replaced
// wholesale on every check-in. Putting "last fired at" there would reset every
// job's history roughly every two minutes, and a daily job would then fire on
// the first tick after every single check-in — a backup storm produced entirely
// by storing a fact in the wrong place.
//
// ---------------------------------------------------- catch-up semantics
//
// If the service was down for three days, a daily job has three missed
// occurrences. It fires ONCE and the state advances to the most recent
// occurrence, not the oldest. Firing three times would back the same machine
// up three times in a row to catch up on backups whose moment has passed, and
// the only one anybody wants is the current state of the disk.
//
// A job seen for the FIRST time does not fire for occurrences already in the
// past. Its state is seeded at "now", so delivering a job at 14:00 whose
// schedule says 02:00 waits for tomorrow's 02:00 rather than running
// immediately. Otherwise every job an MSP authored during the working day
// would start a backup on every machine in the org the moment it was saved.

const managedStateFile = "managed_job_state.json"

var (
	managedStateMu sync.Mutex
	managedState   map[string]time.Time // job id -> last occurrence acted on
)

func getManagedStatePath() (string, error) {
	dir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, managedStateFile), nil
}

// loadManagedState restores the fire history. A missing or unreadable file
// means every job is seen fresh, which seeds rather than back-fires.
func loadManagedState() {
	managedStateMu.Lock()
	defer managedStateMu.Unlock()
	if managedState != nil {
		return
	}
	managedState = map[string]time.Time{}

	path, err := getManagedStatePath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		writeDebugLog("[managed-sched] discarding unreadable fire history")
		return
	}
	for id, ts := range raw {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			managedState[id] = t
		}
	}
}

// saveManagedStateLocked persists the history. Caller holds the lock.
func saveManagedStateLocked() {
	path, err := getManagedStatePath()
	if err != nil {
		return
	}
	raw := make(map[string]string, len(managedState))
	for id, t := range managedState {
		raw[id] = t.Format(time.RFC3339)
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return
	}
	if err := atomicWriteFile(path, data, 0600); err != nil {
		writeErrorLog(fmt.Sprintf("[managed-sched] could not persist fire history: %v", err))
	}
}

// managedJobLocation resolves the zone a job's schedule is read in.
//
// The agent tier of V4-CLIENT-CONFIG.md §4. The job's own pin and the org's
// default are resolved SERVER-side and arrive already applied in
// ManagedJob.Timezone, so what is left here is the machine's own zone and the
// UTC baseline.
//
// An unrecognised name falls back to local rather than failing the job: a
// stale IANA identifier must not stop a machine backing up.
func managedJobLocation(job controlplane.ManagedJob) *time.Location {
	if job.Timezone != "" {
		if loc, err := time.LoadLocation(job.Timezone); err == nil {
			return loc
		}
		writeDebugLog(fmt.Sprintf("[managed-sched] job %q names unknown timezone %q; using this machine's zone",
			job.Name, job.Timezone))
	}
	return time.Local
}

// dueManagedJobs returns the jobs whose schedule has come round since they
// last fired, and the occurrence each is firing for.
//
// Pure apart from the state map, and the clock is a parameter, so the whole
// catch-up and seeding behaviour is testable without waiting for 02:00.
func dueManagedJobs(jobs []controlplane.ManagedJob, state map[string]time.Time, now time.Time) (
	due []controlplane.ManagedJob, fired map[string]time.Time, seeded map[string]time.Time,
) {
	fired = map[string]time.Time{}
	seeded = map[string]time.Time{}

	for _, job := range jobs {
		if job.Schedule == "" {
			continue // trigger-only: it runs when the server asks
		}
		id := fmt.Sprintf("managed-%d", job.ID)

		sched, err := controlplane.ParseCalendar(job.Schedule)
		if err != nil {
			// The server validated this with the same grammar before
			// storing it, so this is a contract violation rather than a
			// user error. Skip the job; do not take the machine's other
			// jobs down with it.
			writeDebugLog(fmt.Sprintf("[managed-sched] job %q has an unparseable schedule %q: %v",
				job.Name, job.Schedule, err))
			continue
		}

		last, seen := state[id]
		if !seen {
			// First sight: seed, do not fire. See the header.
			seeded[id] = now
			continue
		}

		loc := managedJobLocation(job)

		// Walk forward from the last occurrence acted on, taking the LATEST
		// one that is not in the future. That collapses a missed window into
		// a single run instead of one run per missed occurrence.
		latest := time.Time{}
		cursor := last
		for i := 0; i < 512; i++ {
			next := sched.NextOccurrence(loc, cursor)
			if next.IsZero() || next.After(now) {
				break
			}
			latest = next
			cursor = next
		}
		if latest.IsZero() {
			continue
		}
		due = append(due, job)
		fired[id] = latest
	}
	return due, fired, seeded
}

// checkManagedJobs is the managed half of the scheduler tick.
func (a *App) checkManagedJobs() {
	jobs := currentManagedJobs()
	if len(jobs) == 0 {
		return
	}
	loadManagedState()

	managedStateMu.Lock()
	snapshot := make(map[string]time.Time, len(managedState))
	for k, v := range managedState {
		snapshot[k] = v
	}
	managedStateMu.Unlock()

	due, fired, seeded := dueManagedJobs(jobs, snapshot, time.Now())

	if len(fired) == 0 && len(seeded) == 0 {
		return
	}

	// The state is advanced BEFORE the job is dispatched, deliberately. If it
	// were advanced after, a backup that takes longer than the tick interval
	// would still look due on the next tick and be started again — the
	// runningJobs guard in executeScheduledJob would refuse the duplicate,
	// but the log would fill with refusals and any future change to that
	// guard would turn into concurrent backups of one machine.
	managedStateMu.Lock()
	for id, t := range fired {
		managedState[id] = t
	}
	for id, t := range seeded {
		managedState[id] = t
	}
	saveManagedStateLocked()
	managedStateMu.Unlock()

	for _, id := range sortedKeys(seeded) {
		writeDebugLog(fmt.Sprintf("[managed-sched] %s seen for the first time; waiting for its next occurrence", id))
	}

	for _, job := range due {
		sj := managedToScheduledJob(job)
		writeDebugLog(fmt.Sprintf("[managed-sched] firing %s (%s) for %s",
			sj.ID, job.Name, fired[sj.ID].Format(time.RFC3339)))
		// requestID stays empty: nobody asked for this, the schedule came
		// round. The unmanaged-backup gate exempts managed ids, so an org
		// that set restrict_unmanaged_backups still gets its own jobs.
		go a.executeScheduledJob(sj, "")
	}
}

func sortedKeys(m map[string]time.Time) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Deterministic log order; map iteration order would make two identical
	// runs produce differently-ordered logs and complicate any diffing.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
