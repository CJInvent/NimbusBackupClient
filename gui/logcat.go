package main

// Log categories with per-launch verbosity. Detailed diagnostic lines (session
// handshakes, chunk-level tracing, ACL/posture detail) are tagged with a
// category and suppressed unless that category is enabled at launch, e.g.:
//
//	NimbusBackup.exe -logcat pbs,chunks
//	NimbusBackup.exe -logcat all
//
// This is the groundwork for "detailed logs mappable under a log category":
// callers use writeCatLog(catXxx, ...) for verbose lines; always-on
// operational lines keep using writeDebugLog. Default (no flag) = quiet, so we
// never regress the log-volume fixes.

import (
	"strings"
	"sync"
)

type logCategory string

const (
	catPBS      logCategory = "pbs"      // PBS protocol / session handshakes
	catChunks   logCategory = "chunks"   // per-chunk upload tracing
	catSecurity logCategory = "security" // ACL / crypto / posture detail
	catAPI      logCategory = "api"      // local control API
)

var (
	enabledCategories = map[logCategory]bool{}
	logcatMu          sync.RWMutex
)

// SetLogCategories parses a comma-separated launch value ("all" enables every
// category). Safe to call once at startup before logging begins.
func SetLogCategories(spec string) {
	logcatMu.Lock()
	defer logcatMu.Unlock()
	enabledCategories = map[logCategory]bool{}
	for _, part := range strings.Split(spec, ",") {
		p := strings.ToLower(strings.TrimSpace(part))
		if p == "" {
			continue
		}
		if p == "all" {
			for _, c := range []logCategory{catPBS, catChunks, catSecurity, catAPI} {
				enabledCategories[c] = true
			}
			return
		}
		enabledCategories[logCategory(p)] = true
	}
}

func categoryEnabled(c logCategory) bool {
	logcatMu.RLock()
	defer logcatMu.RUnlock()
	return enabledCategories[c]
}

// writeCatLog logs only when the category was enabled at launch.
func writeCatLog(c logCategory, message string) {
	if categoryEnabled(c) {
		writeDebugLog("[" + string(c) + "] " + message)
	}
}
