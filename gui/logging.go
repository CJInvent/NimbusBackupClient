package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// The client's ONE logging module (V4-SPEC §12).
//
// WHAT IT REPLACES: logging_gui.go and logging_service.go, 158 lines each,
// byte-identical apart from the string "gui" or "service" in four filenames.
// A §6 duplicate-implementation violation that predates v4 — and the usual
// consequence of one: a fix to the GUI copy was a fix the service did not get,
// on the build that runs on every managed machine, exactly as with the two
// backup pipelines (docs/V4-PIPELINE.md §1).
//
// The only genuinely per-build fact is which name the files carry, so that is
// all the build tags decide now: `logVariant`, one line each in
// logging_variant_gui.go and logging_variant_service.go.
//
// THE FILENAMES ARE UNCHANGED, including `service-service.log`, which reads
// oddly and is what the service build has always written. Renaming would
// orphan the logs on every installed machine and break the paths the GUI's
// viewer already knows. A cosmetic name is not worth a support call about
// missing logs.
//
// ------------------------------------------------------------------ level
//
// §12 requires the client level to come from ONE documented registry value
// read by ONE centralized module. That is here.
//
//	Path:     HKLM\SOFTWARE\NimbusBackup
//	Name:     LogLevel
//	Type:     REG_SZ
//	Values:   TRACE, DEBUG, INFO (case-insensitive)
//	Default:  INFO — used when the value is absent, empty, or unrecognised
//	Effect:   read once at process start; a change needs a service restart
//
// HKLM rather than HKCU: the service runs as LocalSystem and would never see
// a per-user value, and a level any logged-on user can change is not a
// managed setting.
//
// An unrecognised value logs at INFO rather than failing or going silent. A
// typo in a registry value must not be able to turn off logging on a machine,
// and must not stop it backing up either.
//
// WARN AND ERROR ARE SEVERITY LABELS HERE, NOT FILTER THRESHOLDS, and that is
// a deliberate departure from the server.
//
// The obvious plan was to classify the ~280 operational call sites and then
// offer WARN and ERROR as settable levels. Classification was done — see
// writeWarnLog and writeErrorLog below — but it CANNOT BE CERTIFIED COMPLETE,
// for a reason that is structural rather than a matter of effort: eight call
// sites pass a VARIABLE, `writeDebugLog(msg)`. Whether that line is narration
// or a failure depends on what the caller put in it, and no amount of reading
// this file tells you.
//
// So the settable level stops at INFO. A missed failure staying at INFO is
// harmless when INFO is never suppressed; the same miss under a settable ERROR
// would hide the one line that mattered, on a machine an administrator had
// quietened precisely because they expected it to still report failures.
// Offering a threshold whose safety I cannot demonstrate is how a setting
// becomes a trap.
//
// What the labels buy without that risk: severity is visible in the file and
// greppable, so a support bundle can be triaged by reading `[ERROR]` lines
// first, and monitoring can key on them. If the variable-message call sites
// are ever given explicit severities, the thresholds can be added — here and
// nowhere else.
//
// LEVELS AND CATEGORIES ARE ONE SYSTEM, not two. logcat.go's categories are
// per-launch flags for verbose subsystems (pbs, chunks, security, api); the
// level is the persistent setting. Rather than leave an operator with two
// unrelated verbosity knobs — the shape of problem this file exists to remove
// — DEBUG or lower turns every category on. A launch flag can still enable one
// category without raising the level for everything.

// Log levels, ordered. Deliberately the same names and order as the server's
// Core\Log, so "set logging to DEBUG" means one thing across the product.
type logLevel int

const (
	levelTrace logLevel = 10
	levelDebug logLevel = 20
	levelInfo  logLevel = 30

	// Above INFO, and therefore never suppressed by any settable level.
	// These are labels, not thresholds — see the note above.
	levelWarn  logLevel = 40
	levelError logLevel = 50
)

const logLevelValueName = "LogLevel"

var logLevelNames = map[string]logLevel{
	"TRACE": levelTrace,
	"DEBUG": levelDebug,
	"INFO":  levelInfo,
}

var (
	backupLogger  *RotatingLogger
	serviceLogger *RotatingLogger
	logDir        string

	// currentBackupLogger is set for the duration of a backup run so logs for
	// that run land in a dedicated file. writeBackupLog uses this if set.
	currentBackupLogger   *RotatingLogger
	currentBackupLoggerMu sync.RWMutex

	// activeLevel is read once at startup. Not guarded by a mutex because it
	// is written exactly once, in init, before any goroutine exists.
	activeLevel = levelInfo
)

func init() {
	if runtime.GOOS == "windows" {
		programData := os.Getenv("ProgramData")
		if programData == "" {
			programData = "C:\\ProgramData"
		}
		logDir = filepath.Join(programData, "NimbusBackup")
	} else {
		logDir = "/var/log/nimbusbackup"
	}
	// #nosec G703 -- ProgramData is a trusted Windows system environment variable
	_ = os.MkdirAll(logDir, 0700)

	activeLevel = resolveLogLevel(readNimbusString(logLevelValueName))

	var err error
	backupLogger, err = NewRotatingLogger(
		filepath.Join(logDir, "backup-"+logVariant+".log"),
		MaxLogSize,
		MaxLogFiles,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create backup logger: %v\n", err)
	}

	serviceLogger, err = NewRotatingLogger(
		filepath.Join(logDir, "service-"+logVariant+".log"),
		MaxLogSize,
		MaxLogFiles,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create service logger: %v\n", err)
	}

	// Compress any per-run or rotated log files left behind by a previous
	// crash/kill so operators get a full, inspectable .gz.
	RecoverOrphanLogs(logDir)
}

// resolveLogLevel maps a configured name to a level.
//
// Pure and exported to the package so the fallback behaviour is testable
// without a registry: the case that matters is a typo, and the requirement is
// that it degrades to INFO rather than to silence.
func resolveLogLevel(name string) logLevel {
	if lvl, ok := logLevelNames[strings.ToUpper(strings.TrimSpace(name))]; ok {
		return lvl
	}
	return levelInfo
}

// logLevelEnabled reports whether a level is written.
func logLevelEnabled(l logLevel) bool { return l >= activeLevel }

// GetServiceLogPath returns the path to the service log file
func GetServiceLogPath() string {
	return filepath.Join(logDir, "service-"+logVariant+".log")
}

// GetBackupLogPath returns the path to the backup log file
func GetBackupLogPath() string {
	return filepath.Join(logDir, "backup-"+logVariant+".log")
}

// StartBackupRunLog creates a dedicated log file for the current backup run and
// installs it as the active backup logger. Returns the new logger so the caller
// can close/compress it at the end of the run.
// If the backup produces > MaxLogSize bytes of log, the file is rotated internally.
func StartBackupRunLog(backupID string) *RotatingLogger {
	timestamp := time.Now().Format("20060102-150405")
	name := fmt.Sprintf("backup-%s-%s.log", timestamp, sanitizeForFilename(backupID))
	path := filepath.Join(logDir, name)

	logger, err := NewRotatingLogger(path, MaxLogSize, MaxLogFiles)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create per-run backup logger %s: %v\n", path, err)
		return nil
	}

	currentBackupLoggerMu.Lock()
	currentBackupLogger = logger
	currentBackupLoggerMu.Unlock()
	return logger
}

// EndBackupRunLog closes the per-run backup logger and compresses the log file.
// Safe to call with a nil logger.
func EndBackupRunLog(logger *RotatingLogger) {
	if logger == nil {
		return
	}

	currentBackupLoggerMu.Lock()
	if currentBackupLogger == logger {
		currentBackupLogger = nil
	}
	currentBackupLoggerMu.Unlock()

	path := logger.path
	_ = logger.Close()

	// Synchronous compression: after a multi-hour backup, an extra second
	// to flush a gzip footer is acceptable, and it guarantees that a
	// service stop/kill right after EndBackupRunLog returns can't leave a
	// truncated .log.gz on disk.
	if err := compressLogFile(path); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to compress backup run log %s: %v\n", path, err)
	}
}

// writeDebugLog writes to the service log (scheduler, general operations).
//
// INFO by level, which is what its ~280 call sites are: operational lines
// somebody reading a support bundle expects to find. Verbose diagnostics go
// through writeCatLog instead, and are DEBUG.
func writeDebugLog(message string) {
	if !logLevelEnabled(levelInfo) {
		return
	}
	writeLogToLogger(serviceLogger, "SERVICE", message)
}

// writeWarnLog records something degraded but survivable: a fallback taken, a
// retry, a capability missing, a value skipped.
//
// Never suppressed. WARN is above INFO and the settable level stops at INFO,
// so raising the level cannot hide these — which is what makes labelling them
// safe even though the classification is incomplete.
func writeWarnLog(message string) {
	writeLogToLogger(serviceLogger, "WARN", message)
}

// writeErrorLog records an operational failure.
//
// Never suppressed, for the same reason as writeWarnLog. If a line that
// belongs here is still going through writeDebugLog, the consequence is that
// it is labelled SERVICE rather than ERROR — mislabelled, not lost.
func writeErrorLog(message string) {
	writeLogToLogger(serviceLogger, "ERROR", message)
}

// writeBackupLog writes to backup log (backup operations).
// Prefers the per-run logger if one is active, else falls back to the global.
//
// NOT level-gated. A backup's own log is the record of that backup, and an
// agent quietened to WARN must still produce a usable one — the level exists
// to control diagnostic noise, not to discard the artifact an operator opens
// when a backup fails.
func writeBackupLog(message string) {
	currentBackupLoggerMu.RLock()
	perRun := currentBackupLogger
	currentBackupLoggerMu.RUnlock()

	if perRun != nil {
		writeLogToLogger(perRun, "BACKUP", message)
		return
	}
	writeLogToLogger(backupLogger, "BACKUP", message)
}

func writeLogToLogger(logger *RotatingLogger, prefix string, message string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	logLine := fmt.Sprintf("[%s] [%s] %s\n", prefix, timestamp, redactLogLine(message))

	// Fallback to stderr if logger is not initialized
	if logger == nil {
		fmt.Fprint(os.Stderr, logLine)
		return
	}

	// Write to rotating logger
	if err := logger.Write(logLine); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write log: %v\n", err)
		fmt.Fprint(os.Stderr, logLine)
	}

	// Also write to stderr for console output
	fmt.Fprint(os.Stderr, logLine)
}
