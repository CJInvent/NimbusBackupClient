//go:build !service
// +build !service

package main

import (
	"testing"

	"github.com/wailsapp/wails/v2/pkg/options"
)

// F-44: the GUI is single-instance through Wails' lock, with a handler that
// shows the existing window. (The broken hand-made mutex is gone; the x/sys
// contract Wails relies on is pinned by single_instance_windows_test.go.)
func TestGUIIsSingleInstance(t *testing.T) {
	app := &App{}
	o := guiOptions(app, false, t.TempDir())
	if o.SingleInstanceLock == nil {
		t.Fatal("the GUI must configure Wails' SingleInstanceLock")
	}
	if o.SingleInstanceLock.UniqueId != guiSingleInstanceID || guiSingleInstanceID == "" {
		t.Fatalf("unexpected lock id %q", o.SingleInstanceLock.UniqueId)
	}
	if o.SingleInstanceLock.OnSecondInstanceLaunch == nil {
		t.Fatal("a second launch must reach a handler in the first instance")
	}
	// Without a Wails context (not started yet) the handler must not panic.
	o.SingleInstanceLock.OnSecondInstanceLaunch(options.SecondInstanceData{})
}
