//go:build service

package main

// Only the service delivers WARN/ERROR lines to the control plane: it is the
// process that checks in. See logqueue.go.
func init() { logQueueEnabled = true }
