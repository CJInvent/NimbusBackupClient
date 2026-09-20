//go:build service && windows

package main

import (
	"controlplane"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestRunHistoryValidationFailureTerminatesAnnouncedRun(t *testing.T) {
	reports := make(chan controlplane.RunReport, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report controlplane.RunReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Error(err)
		}
		reports <- report
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	previousRegistry := currentRunRegistry()
	previousClient, previousPending := cpClient, cpPendingRep
	defer func() { SetRunRegistry(previousRegistry); cpClient = previousClient; cpPendingRep = previousPending }()
	cpClient = &controlplane.Client{BaseURL: server.URL, AgentID: 1, Secret: "test-only"}
	pending := cpClient.NewRun("Nightly", "machine")
	cpPendingRep = pending
	reg := api.NewRunRegistry()
	SetRunRegistry(reg)
	id := reg.Begin(api.TriggerSchedule, "job-1", "Nightly", "validation-proof", "machine")
	app := &App{} // No configuration: validation fails before engine/hook setup.
	err := app.runBackupPipeline(backupRequest{BackupType: "machine", BackupID: "validation-proof"})
	if err == nil {
		t.Fatal("invalid request unexpectedly succeeded")
	}
	run, ok := reg.Get(id)
	if !ok || run.Success || run.Error != err.Error() {
		t.Fatalf("local terminal refusal lost: %+v", run)
	}
	select {
	case report := <-reports:
		if report.Status != "failed" || report.RunUUID != pending.RunUUID() {
			t.Fatalf("announced server run not finalized: %+v", report)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no terminal server report")
	}
	if cpPendingRep != nil {
		t.Fatal("failed reporter leaked into the next run")
	}
}
