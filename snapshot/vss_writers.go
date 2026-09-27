package snapshot

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Reading `vssadmin list writers` in ANY display language (V4-BETA-FIXES §4,
// ledger F-36).
//
// The previous parser matched the English labels "Writer name:", "State:" and
// "Last error:" and compared the error to "No error". vssadmin is localized,
// so on a French or Spanish Windows nothing matched -- or, where a label
// happened to match, every writer looked failed. The structure is what does
// not change with the language, so that is all this reads:
//
//   - a writer starts at a line whose value (after the first ':') is a
//     single-quoted name (writer names are not localized);
//   - its state is the bracketed number on a later line, "[7] Failed": the
//     VSS_WRITER_STATE value, whatever the words after it say;
//   - its last error is the value of the line after the state line, reported
//     as given and never used to decide anything.
//
// A writer counts as FAILED when its state is 6 or higher
// (VSS_WS_FAILED_AT_IDENTIFY and every later failure state).

// WriterState is one writer as vssadmin reported it.
type WriterState struct {
	Name      string
	State     int    // VSS_WRITER_STATE
	StateText string // the words after the number, in the display language
	LastError string // as given; not used to decide
}

// Failed reports whether the writer is in a failure state.
func (w WriterState) Failed() bool { return w.State >= vssWriterFirstFailedState }

// vssWriterFirstFailedState is VSS_WS_FAILED_AT_IDENTIFY; every state from it
// up is a failure (vswriter.h).
const vssWriterFirstFailedState = 6

var stateValueRe = regexp.MustCompile(`^\[(\d+)\]\s*(.*)$`)

// value is what follows the first ':' on a line, trimmed.
func value(line string) (string, bool) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return "", false
	}
	return strings.TrimSpace(line[i+1:]), true
}

// parseWriters reads vssadmin's writer listing. Lines that belong to no writer
// (the banner, blank lines) are ignored.
func parseWriters(out string) []WriterState {
	var ws []WriterState
	cur := -1
	expectError := false
	for _, raw := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		v, ok := value(raw)
		if !ok {
			expectError = false
			continue
		}
		if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			ws = append(ws, WriterState{Name: v[1 : len(v)-1], State: -1})
			cur = len(ws) - 1
			expectError = false
			continue
		}
		if cur < 0 {
			continue
		}
		if m := stateValueRe.FindStringSubmatch(v); m != nil && ws[cur].State < 0 {
			n, _ := strconv.Atoi(m[1])
			ws[cur].State, ws[cur].StateText = n, m[2]
			expectError = true
			continue
		}
		if expectError {
			ws[cur].LastError = v
			expectError = false
		}
	}
	return ws
}

// failedWriters are the writers in a failure state.
func failedWriters(ws []WriterState) []WriterState {
	var out []WriterState
	for _, w := range ws {
		if w.Failed() {
			out = append(out, w)
		}
	}
	return out
}

// busyWriters are writers in a WAITING state (2-5): a snapshot is in flight
// somewhere, which is what "another program is creating a shadow copy" looks
// like from the writers' side.
func busyWriters(ws []WriterState) []string {
	var out []string
	for _, w := range ws {
		if w.State >= 2 && w.State < vssWriterFirstFailedState {
			out = append(out, fmt.Sprintf("'%s' [%d] %s", w.Name, w.State, w.StateText))
		}
	}
	return out
}

// WriterNames lists the names of ws.
func WriterNames(ws []WriterState) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Name)
	}
	return out
}
