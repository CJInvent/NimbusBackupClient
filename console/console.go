package main

// The console: what the webview is bound to (docs/V4-CONSOLE.md, phase C2).
//
// Every exported method of Console is a binding the front end calls, and every
// binding is one request to the service's local API, or a fact the console
// owns itself (its version, its tray, its operating system, until
// V4-LOCAL-API P3 the folder picker). It holds no configuration, no secret
// and no file the service writes; when the service does not answer, a binding
// returns the error the screens already show and does nothing else.
//
// WHY A TYPE OF ITS OWN, AND WHY NOW. The GUI used to bind App, the service's
// own type, so Wails made every exported App method callable from the webview:
// 83 of them, 31 never called by any screen, including the service's entry
// points (StartControlPlane, ApplyProvisioningProfile, SaveConfigFromMap ...).
// Several App methods were both a binding and the service's implementation,
// with an isServiceProcess branch deciding which: App.SaveScheduledJob wrote
// scheduled_jobs.json in either process. Binding a separate type ends both
// problems at once: the bound surface is exactly what is written here (pinned
// by console_bindings_test.go against frontend/src/lib/bindings.js), and App
// is the service's alone. Since phase C3 this is the console's own module,
// which cannot import the service's code at all.

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// Console is the bound type. Its fields are a context, a client and the
// console's own state; nothing the service owns.
type Console struct {
	ctx context.Context
	api *api.Client

	// up is true once the service has answered. Writes check it before
	// sending, so a service that is down gives the coded NB-2011 the screens
	// translate rather than a dial error; a service that started after the
	// console is found again on the next write.
	up atomic.Bool

	// lastVolumeTruncated remembers whether the service's last partition scan
	// hit its entry cap, so LastVolumeListTruncated answers without a second
	// round trip. The scan itself lives in the service.
	lastVolumeTruncated atomic.Bool
}

// NewConsole is the console of this machine's service.
func NewConsole() *Console {
	return &Console{api: api.NewClient(api.TokenPath())}
}

// service returns the client once the service has answered, or the coded
// "service unavailable" error. The probe is GET /status, made only until the
// first answer.
func (c *Console) service() (*api.Client, error) {
	if c.api == nil {
		return nil, errors.New(errServiceUnavailable)
	}
	if c.up.Load() {
		return c.api, nil
	}
	if !c.api.IsServiceAvailable() {
		return nil, errors.New(errServiceUnavailable)
	}
	writeInfoLog("[Console] the service is reachable")
	c.up.Store(true)
	return c.api, nil
}

// startup is Wails' OnStartup.
func (c *Console) startup(ctx context.Context) {
	c.ctx = ctx
	writeInfoLog("Console.startup() called")

	if _, err := c.service(); err != nil {
		writeInfoLog("Service unreachable at startup - the console shows status only until it comes back")
	}
	go c.setupSystemTray()
}

// domReady is Wails' OnDomReady.
func (c *Console) domReady(context.Context) {
	writeInfoLog("Console.domReady() called - UI loaded successfully")
}

// beforeClose is Wails' OnBeforeClose: closing the window hides it to the tray.
func (c *Console) beforeClose(context.Context) (prevent bool) {
	writeInfoLog("Console.beforeClose() called - minimizing to tray")
	c.minimizeToTray()
	return true
}

// shutdown is Wails' OnShutdown. The console holds no session with anything
// but the service, so there is nothing to close.
func (c *Console) shutdown(context.Context) {
	writeInfoLog("Console.shutdown() called")
}
