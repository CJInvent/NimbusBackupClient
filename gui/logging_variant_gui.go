//go:build !service
// +build !service

package main

// logVariant is the ONLY thing about logging that differs between the two
// builds: which name the log files carry. Everything else lives in
// logging.go, unduplicated (V4-SPEC §12).
const logVariant = "gui"
