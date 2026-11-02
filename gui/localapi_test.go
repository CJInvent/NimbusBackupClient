package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestTheServiceDoesNotStartWithoutItsPipe: the local API's pipe is created
// by Start, and failing to create it (another process holds the name, the
// spec's squatter) fails the start. The pipe's refusal itself is gui/api's
// TestAProcessHoldingThePipeNameStopsTheService; this is the service acting
// on it.
func TestTheServiceDoesNotStartWithoutItsPipe(t *testing.T) {
	held := errors.New("the pipe name is held by another process")
	orig := listenLocalAPI
	listenLocalAPI = func() (net.Listener, error) { return nil, held }
	t.Cleanup(func() { listenLocalAPI = orig })

	s := &NimbusService{}
	err := s.Start(nil)
	if !errors.Is(err, held) {
		t.Fatalf("Start behind a held pipe returned %v, want the listen error", err)
	}
	if s.stopChan != nil || s.apiListener != nil {
		t.Error("Start went on to set the service up after its pipe failed")
	}
}

func TestTheRetiredTokenFileIsRemoved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)
	data, err := getConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, retiredAPITokenFile)
	if err := os.WriteFile(path, []byte("left by an older version"), 0o600); err != nil {
		t.Fatal(err)
	}
	removeRetiredAPIToken()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the retired token file is still there (%v)", err)
	}
	// Nothing to remove is not an error, and does not create anything.
	removeRetiredAPIToken()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a second pass created %s", path)
	}
}
