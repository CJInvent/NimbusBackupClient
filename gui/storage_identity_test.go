package main

import (
	"strings"
	"testing"
)

func TestStorageJoinedApprovalRefused(t *testing.T) {
	a := &App{config: &Config{ControlServerURL: "https://control.invalid", ControlAgentID: 1}, isServiceProcess: true}
	if err := a.ApproveStorageIdentityFromMap(map[string]interface{}{}); err == nil || !strings.Contains(err.Error(), "dashboard") {
		t.Fatalf("joined local API approval not refused: %v", err)
	}
}

func TestStorageGUIWithoutServiceCannotApproveOrWriteState(t *testing.T) {
	a := &App{config: &Config{}}
	if a.ApproveStorageIdentity(map[string]interface{}{}) == nil {
		t.Fatal("GUI approved without service")
	}
	if _, err := a.storageController(); err == nil {
		t.Fatal("GUI acquired state writer")
	}
}
