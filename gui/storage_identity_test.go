package main

import (
	"controlplane"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStorageJoinedApprovalRefused(t *testing.T) {
	a := &App{config: &Config{ControlServerURL: "https://control.invalid", ControlAgentID: 1}, isServiceProcess: true}
	if err := a.ApproveStorageIdentityFromMap(map[string]interface{}{}); err == nil || !strings.Contains(err.Error(), "dashboard") {
		t.Fatalf("joined local API approval not refused: %v", err)
	}
}

// An App that is not the service process never acquires the state writer.
// (The console has no App at all since V4-CONSOLE C2; its approval is a local
// API call, refused without a service: console_bindings_test.go.)
func TestStorageNonServiceAppCannotWriteState(t *testing.T) {
	a := &App{config: &Config{}}
	if _, err := a.storageController(); err == nil {
		t.Fatal("a non-service App acquired the state writer")
	}
}

func TestStorageFailureRemainsVisibleAndValidTelemetry(t *testing.T) {
	a := &App{config: &Config{}}
	fault := "state ACL failed\n" + strings.Repeat("é", 600)
	st := a.rememberStorageFailure(controlplane.StorageStatus{}, fault)
	if len(st.Observation) != 64 || len(st.Error) > 512 || strings.Contains(st.Error, "\n") || !utf8.ValidString(st.Error) {
		t.Fatalf("invalid fault telemetry: %#v", st)
	}
	// GUI/status reads must not turn a failed refresh into an apparently healthy snapshot.
	status := a.StorageIdentityStatusMap()
	if status["error"] != st.Error || status["observation"] != st.Observation {
		t.Fatal("status lost pending persistence failure")
	}
	again := a.rememberStorageFailure(controlplane.StorageStatus{}, "second failure")
	if again.Error != st.Error {
		t.Fatal("original unresolved fault was replaced")
	}
}
