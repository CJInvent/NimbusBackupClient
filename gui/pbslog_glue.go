package main

import (
	"pbscommon"
	"snapshot"
)

// pbslog_glue.go — routes pbscommon's diagnostic hook into the app log, for
// both processes. Kept in its own file so the wiring is obvious and single.

func init() {
	pbscommonSetDebugLog()
}

func pbscommonSetDebugLog() {
	pbscommon.DebugLogFn = func(msg string) { writeDebugLog("[pbs] " + msg) }
	snapshot.LogFn = func(msg string) { writeDebugLog(msg) }
}
