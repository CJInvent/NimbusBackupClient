package controlplane

import (
	"fmt"
	"log"
	"sync"
)

// Leveled diagnostics for this package.
//
// WHY THIS EXISTS. Every line this package wrote went through the stdlib
// `log` package, which the host process redirects into its rotating log at
// INFO. So "check-in failed", "run report failed" and "command handler
// panicked" were filed beside "command dispatched OK" at the same level: a
// support bundle triaged by reading [ERROR] lines first never met them, and
// the WARN/ERROR push to the control plane (V4-RUN-AUDIT §4.1) -- which is
// keyed on level -- would never have carried a single one of them.
//
// The package does not own the log file; the process embedding it does. So
// it exposes one sink the host installs, and falls back to the stdlib logger
// (still redirected, still visible) when nothing was installed -- a library
// whose failure mode is silence is the defect logredirect.go was written for.

// LogLevel is the severity of one line from this package.
type LogLevel int

const (
	LogDebug LogLevel = iota
	LogInfo
	LogWarn
	LogError
)

func (l LogLevel) String() string {
	switch l {
	case LogDebug:
		return "DEBUG"
	case LogInfo:
		return "INFO"
	case LogWarn:
		return "WARN"
	default:
		return "ERROR"
	}
}

var (
	logSinkMu sync.RWMutex
	logSink   func(LogLevel, string)
)

// SetLogger installs the host's leveled logger. Messages arrive already
// prefixed with "[controlplane]".
func SetLogger(f func(LogLevel, string)) {
	logSinkMu.Lock()
	logSink = f
	logSinkMu.Unlock()
}

func logf(l LogLevel, format string, args ...interface{}) {
	msg := "[controlplane] " + fmt.Sprintf(format, args...)
	logSinkMu.RLock()
	f := logSink
	logSinkMu.RUnlock()
	if f != nil {
		f(l, msg)
		return
	}
	log.Print(l.String() + " " + msg)
}

// repeatGate collapses a condition that recurs on every cycle into one line
// when it starts, silence while it repeats, and one line when it changes or
// clears (V4-RUN-AUDIT §5 rule 5). A server outage used to write "check-in
// failed" every two minutes for as long as it lasted; an operator reading the
// log afterwards needs to know when it started, what it said, and when it
// ended -- not 700 copies of the middle.
type repeatGate struct {
	mu      sync.Mutex
	current string
	repeats int
}

// fail reports one occurrence. It returns the line to emit, or "" to stay
// quiet. A DIFFERENT message is a state change and is always emitted, with the
// count of the one it replaced.
func (g *repeatGate) fail(msg string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.current == msg {
		g.repeats++
		return ""
	}
	out := msg
	if g.current != "" && g.repeats > 0 {
		out = fmt.Sprintf("%s (previous failure repeated %d more time(s))", msg, g.repeats)
	}
	g.current, g.repeats = msg, 0
	return out
}

// ok reports that the condition cleared. It returns the recovery line, or ""
// when nothing was failing.
func (g *repeatGate) ok() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.current == "" {
		return ""
	}
	out := fmt.Sprintf("recovered after %d consecutive failure(s); last: %s", g.repeats+1, g.current)
	g.current, g.repeats = "", 0
	return out
}
