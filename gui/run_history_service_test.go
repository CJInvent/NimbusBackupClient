//go:build service && windows

package main

import (
	"errors"
	"testing"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

func TestRunHistoryKeepsScheduledIdentityAndRefusal(t *testing.T) {
	previous := currentRunRegistry()
	reg := api.NewRunRegistry()
	SetRunRegistry(reg)
	t.Cleanup(func() { SetRunRegistry(previous) })
	id := reg.Begin(api.TriggerSchedule, "job-1", "Nightly", "test-id", "machine")
	opts := BackupOptions{BackupID: "test-id", BackupType: "vm"}
	finish, name := attachRunRegistry(&opts)
	if name != "Nightly" {
		t.Fatalf("history named %q", name)
	}
	finish(errors.New("required backup key unavailable"))
	run, ok := reg.Get(id)
	if !ok || run.Success || run.Error != "required backup key unavailable" {
		t.Fatalf("refusal lost: %+v", run)
	}
	finish(nil)
	run, _ = reg.Get(id)
	if run.Success {
		t.Fatal("terminal refusal overwritten by success")
	}
}
