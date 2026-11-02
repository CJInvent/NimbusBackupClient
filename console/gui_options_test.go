package main

import (
	"strings"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/options"
)

// F-44: the GUI is single-instance through Wails' lock, with a handler that
// shows the existing window. (The broken hand-made mutex is gone; the x/sys
// contract Wails relies on is pinned by single_instance_windows_test.go.)
func TestGUIIsSingleInstance(t *testing.T) {
	o := guiOptions(&Console{}, false, t.TempDir())
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

// The console is built with wv2runtime.error (V4-CONSOLE D2): with no runtime
// it shows Messages.ContactAdmin and exits. That sentence must send the user
// to the service, which installs the runtime, not to "your administrator".
func TestAMissingWebView2PointsAtTheService(t *testing.T) {
	o := guiOptions(&Console{}, false, t.TempDir())
	if o.Windows == nil || o.Windows.Messages == nil {
		t.Fatal("the console sets no Windows messages; Wails' default sends the user to their administrator")
	}
	if got := o.Windows.Messages.ContactAdmin; got != webView2Missing || !strings.Contains(got, "service") {
		t.Errorf("ContactAdmin = %q, want webView2Missing", got)
	}
	// The other messages are Wails' own, not blanks.
	if o.Windows.Messages.Error == "" || o.Windows.Messages.Webview2NotInstalled == "" {
		t.Error("setting one message blanked Wails' others")
	}
}
