//go:build !service
// +build !service

package main

// The console's answer to a portal browse command: there isn't one.
//
// cpHandleCommand is compiled into both builds because the control-plane glue
// is shared, but the commands that browse an image backup are executed by the
// engine — and the engine is `service`-only now. This stub is what keeps that
// true at compile time: the console cannot handle those commands because it
// has nothing to handle them with.
//
// It reports handled=false rather than an error, so a command that reaches
// here falls through to the ordinary dispatch and is refused as unknown. That
// is the honest answer: this process does not do that, and saying "failed"
// would suggest it tried.
//
// In practice nothing reaches here at all — the check-in loop that receives
// commands runs in the service (service.go, StartControlPlane).

import "controlplane"

func (a *App) cpHandleBrowseCommand(_ controlplane.Command) (controlplane.CommandResult, bool) {
	return controlplane.CommandResult{}, false
}
