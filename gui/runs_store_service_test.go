//go:build service
// +build service

package main

// The run history file's glue (runs_store_service.go) against a real
// ProgramData directory. Named TestRunHistory* so the Windows smoke step's
// `-tags service` run picks them up; CI's Linux unit job runs them too.

import (
	"os"
	"testing"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

func TestRunHistoryFileSurvivesAServiceRestart(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	path, err := runHistoryPath()
	if err != nil {
		t.Fatal(err)
	}

	// First process: a run finishes, another is cut off by the stop.
	first := api.NewRunRegistry()
	loadRunHistory(first)
	done := first.Begin(api.TriggerSchedule, "job-1", "Nightly", "WS-01", "machine")
	first.Complete(done, true, "Backup completed successfully")
	cut := first.Begin(api.TriggerManual, "", "Manual backup", "WS-01", "directory")

	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		t.Fatalf("no history file at %s after runs began and ended (%v)", path, err)
	}

	// Second process.
	second := api.NewRunRegistry()
	loadRunHistory(second)
	if run, ok := second.Get(done); !ok || !run.Success || run.JobName != "Nightly" {
		t.Errorf("the finished run after restart = %+v (found %v)", run, ok)
	}
	if run, ok := second.Get(cut); !ok || run.Success || run.Error != api.InterruptedRunText {
		t.Errorf("the cut-off run after restart = %+v (found %v); want it closed as interrupted", run, ok)
	}
}

func TestRunHistoryUnreadableFileIsMovedAsideNotOverwritten(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	path, err := runHistoryPath()
	if err != nil {
		t.Fatal(err)
	}
	garbage := []byte("{not the history")
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}

	reg := api.NewRunRegistry()
	loadRunHistory(reg)
	kept, err := os.ReadFile(path + ".unreadable")
	if err != nil || string(kept) != string(garbage) {
		t.Fatalf("the unreadable file was not kept aside: %q, %v", kept, err)
	}
	// The registry carries on and writes a good file.
	id := reg.Begin(api.TriggerManual, "", "", "WS-01", "directory")
	reg.Complete(id, true, "done")
	again := api.NewRunRegistry()
	loadRunHistory(again)
	if _, ok := again.Get(id); !ok {
		t.Error("the new history file written after the bad one was not readable")
	}
}
