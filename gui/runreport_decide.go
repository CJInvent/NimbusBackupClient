package main

import "strings"

// How a finished run is reported to the control plane, decided in one pure
// place. Untagged on purpose: the reporter that acts on it
// (controlplane_runreport.go) is service-only, and a rule that only the
// service build can see is a rule the gate's tests never run.

type runReportKind int

const (
	reportSuccess runReportKind = iota
	reportWarning
	reportFailed
	reportVSSFailed
)

// decideRunReport classifies a result the engine reported itself (OnResult).
// writerSummary is non-empty when VSS writers failed during this run's
// snapshot: the data was captured, application consistency was not, and the
// run is Warning rather than Success (CJ, 2026-09-25; V4-BETA-FIXES §4).
func decideRunReport(s *BackupStatus, writerSummary string) (runReportKind, string) {
	switch {
	case s.Outcome == OutcomeFailed && errorLooksVSS(s.Message):
		return reportVSSFailed, vssCause(firstLine(s.Message))
	case s.Outcome == OutcomeFailed:
		return reportFailed, firstLine(s.Message)
	case len(s.SkippedReadError) > 0 || len(s.Directories) > 0 && anyDirFailed(s.Directories):
		summary := firstLine(s.Message)
		if writerSummary != "" {
			summary = writerSummary + "; " + summary
		}
		return reportWarning, summary
	case writerSummary != "":
		return reportWarning, writerSummary
	default:
		return reportSuccess, ""
	}
}

// decideFinalReport classifies a run the engine did not report itself, from
// its error return (the machine engine never emits OnResult).
func decideFinalReport(err error, writerSummary string) (runReportKind, string) {
	switch {
	case err != nil && errorLooksVSS(err.Error()):
		return reportVSSFailed, vssCause(firstLine(err.Error()))
	case err != nil:
		return reportFailed, firstLine(err.Error())
	case writerSummary != "":
		return reportWarning, writerSummary
	default:
		return reportSuccess, ""
	}
}

// errorLooksVSS classifies a failure as VSS-side: the sentinel from
// backupDirectory wraps every error that occurred before the shadow copy
// was confirmed.
func errorLooksVSS(msg string) bool {
	return strings.Contains(msg, vssCreateFailedMarker)
}

// vssCause is what follows the VSS marker: the snapshot package's readable
// cause. The server prefixes "Snapshot creation failed: " itself, so sending
// the marker too read as the same sentence twice (ledger F-43).
func vssCause(msg string) string {
	if i := strings.LastIndex(msg, vssCreateFailedMarker+": "); i >= 0 {
		return msg[i+len(vssCreateFailedMarker)+2:]
	}
	return msg
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func anyDirFailed(dirs []DirResult) bool {
	for _, d := range dirs {
		if !d.OK {
			return true
		}
	}
	return false
}
