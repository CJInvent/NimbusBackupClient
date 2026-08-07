//go:build service
// +build service

package main

import (
	"testing"
	"time"

	"controlplane"
)

// dueManagedJobs — the seeding and catch-up rules.
//
// The clock is a parameter and the state is passed in, so all of this is
// testable without waiting for 02:00 or touching a file. The two behaviours
// worth breaking the build over are both about NOT starting backups: a job
// seen for the first time must not fire for occurrences already past, and a
// missed window must collapse into ONE run rather than one per occurrence.

func job(id int64, name, schedule string) controlplane.ManagedJob {
	return controlplane.ManagedJob{
		ID: id, Name: name, Schedule: schedule, Timezone: "UTC",
		BackupType: "directory", BackupDirs: []string{`C:\Data`},
	}
}

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

func TestManagedJobSeenFirstTimeDoesNotBackfire(t *testing.T) {
	// An MSP authoring a job at 14:00 whose schedule says 02:00 must not
	// start a backup on every machine in the org the moment they save it.
	jobs := []controlplane.ManagedJob{job(1, "Nightly", "*-*-* 02:00")}
	now := at("2026-08-05 14:00:00")

	due, fired, seeded := dueManagedJobs(jobs, map[string]time.Time{}, now)
	if len(due) != 0 {
		t.Errorf("a newly delivered job fired immediately: %+v", due)
	}
	if len(fired) != 0 {
		t.Errorf("fired = %+v, want none", fired)
	}
	if got, ok := seeded["managed-1"]; !ok || !got.Equal(now) {
		t.Errorf("seeded = %v (ok=%v), want %v", got, ok, now)
	}
}

func TestManagedJobFiresWhenItsTimeComes(t *testing.T) {
	jobs := []controlplane.ManagedJob{job(1, "Nightly", "*-*-* 02:00")}
	state := map[string]time.Time{"managed-1": at("2026-08-05 14:00:00")}

	// Before 02:00 the next day: nothing.
	if due, _, _ := dueManagedJobs(jobs, state, at("2026-08-06 01:59:00")); len(due) != 0 {
		t.Errorf("fired early: %+v", due)
	}

	due, fired, _ := dueManagedJobs(jobs, state, at("2026-08-06 02:00:30"))
	if len(due) != 1 {
		t.Fatalf("got %d due jobs, want 1", len(due))
	}
	if got := fired["managed-1"]; !got.Equal(at("2026-08-06 02:00:00")) {
		t.Errorf("recorded occurrence %v, want 2026-08-06 02:00:00", got)
	}
}

func TestMissedWindowCollapsesToOneRun(t *testing.T) {
	// The service was down for three days. Three occurrences were missed.
	// Backing the machine up three times to catch up on backups whose moment
	// has passed is pointless — the only state anyone wants is the current
	// one — so it fires once and the state jumps to the LATEST occurrence.
	jobs := []controlplane.ManagedJob{job(1, "Nightly", "*-*-* 02:00")}
	state := map[string]time.Time{"managed-1": at("2026-08-02 02:00:00")}

	due, fired, _ := dueManagedJobs(jobs, state, at("2026-08-05 09:00:00"))
	if len(due) != 1 {
		t.Fatalf("got %d due jobs, want exactly 1 after a 3-day outage", len(due))
	}
	if got := fired["managed-1"]; !got.Equal(at("2026-08-05 02:00:00")) {
		t.Errorf("state advanced to %v, want the LATEST missed occurrence 2026-08-05 02:00:00", got)
	}
}

func TestTriggerOnlyJobNeverFiresOnASchedule(t *testing.T) {
	// No schedule means it runs when the server asks. It must not appear
	// due, and it must not even be seeded — there is nothing to track.
	jobs := []controlplane.ManagedJob{job(1, "On demand", "")}
	due, fired, seeded := dueManagedJobs(jobs, map[string]time.Time{}, at("2026-08-05 02:00:00"))
	if len(due) != 0 || len(fired) != 0 || len(seeded) != 0 {
		t.Errorf("a trigger-only job produced due=%v fired=%v seeded=%v", due, fired, seeded)
	}
}

func TestUnparseableScheduleSkipsOnlyThatJob(t *testing.T) {
	// The server validated the expression with the same grammar before
	// storing it, so this is a contract violation. It must not take the
	// machine's other jobs down with it.
	jobs := []controlplane.ManagedJob{
		job(1, "Broken", "every second tuesday"),
		job(2, "Fine", "*-*-* 02:00"),
	}
	state := map[string]time.Time{
		"managed-1": at("2026-08-05 00:00:00"),
		"managed-2": at("2026-08-05 00:00:00"),
	}
	due, _, _ := dueManagedJobs(jobs, state, at("2026-08-05 03:00:00"))
	if len(due) != 1 || due[0].ID != 2 {
		t.Errorf("due = %+v, want only the job with a valid schedule", due)
	}
}

func TestWeekendGapIsRespected(t *testing.T) {
	// mon..fri does not fire on Saturday. 2026-08-08 is a Saturday.
	jobs := []controlplane.ManagedJob{job(1, "Weekdays", "mon..fri 02:00")}
	state := map[string]time.Time{"managed-1": at("2026-08-07 02:00:00")} // Friday

	if due, _, _ := dueManagedJobs(jobs, state, at("2026-08-08 09:00:00")); len(due) != 0 {
		t.Errorf("a mon..fri job fired on Saturday: %+v", due)
	}
	due, fired, _ := dueManagedJobs(jobs, state, at("2026-08-10 02:30:00")) // Monday
	if len(due) != 1 {
		t.Fatalf("got %d due jobs on Monday, want 1", len(due))
	}
	if got := fired["managed-1"]; !got.Equal(at("2026-08-10 02:00:00")) {
		t.Errorf("occurrence %v, want Monday 02:00", got)
	}
}

func TestJobTimezoneIsHonoured(t *testing.T) {
	if _, err := time.LoadLocation("America/New_York"); err != nil {
		t.Skip("tzdata unavailable")
	}
	// 02:00 New York is 06:00 UTC. At 03:00 UTC the job is NOT yet due —
	// which is the whole point of carrying a zone rather than a wall clock.
	j := job(1, "NY nightly", "*-*-* 02:00")
	j.Timezone = "America/New_York"
	jobs := []controlplane.ManagedJob{j}
	state := map[string]time.Time{"managed-1": at("2026-08-05 12:00:00")}

	if due, _, _ := dueManagedJobs(jobs, state, at("2026-08-06 03:00:00")); len(due) != 0 {
		t.Error("fired at 03:00 UTC; 02:00 New York is 06:00 UTC")
	}
	if due, _, _ := dueManagedJobs(jobs, state, at("2026-08-06 06:30:00")); len(due) != 1 {
		t.Error("did not fire at 06:30 UTC, which is past 02:00 New York")
	}
}
