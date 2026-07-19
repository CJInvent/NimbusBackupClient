package logger

import (
	"io"
	"log/slog"
	"os"
	"sync/atomic"
)

// Logger is the interface for structured logging throughout the application
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	With(args ...any) Logger
}

// DefaultLogger wraps slog for structured logging
type DefaultLogger struct {
	logger *slog.Logger
}

// defaultLogger is read on every log call and replaced by Init, so it is held
// in an atomic pointer rather than a plain global: Get() racing an Init() is
// otherwise a data race under -race.
var defaultLogger atomic.Pointer[DefaultLogger]

// Init installs the global logger, replacing any previously installed one.
//
// This used to be wrapped in a sync.Once, which made every call after the
// first a silent no-op: Init(w, level) accepted a writer and a level and threw
// them away, so a process that re-initialized its logger (after loading
// config, or when a service restarts its log sink) kept writing to the ORIGINAL
// destination with no error anywhere. That also broke this package's own test
// suite — four of five tests handed Init a fresh buffer and asserted on output
// that was still going to the first test's buffer. The bug survived because
// nothing in the workspace ran these tests until S1.
func Init(w io.Writer, level slog.Level) {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	defaultLogger.Store(&DefaultLogger{logger: slog.New(handler)})
}

// Get returns the global logger instance, initializing it to stderr if Init
// has not been called.
func Get() Logger {
	if l := defaultLogger.Load(); l != nil {
		return l
	}
	// CompareAndSwap so two racing first-callers cannot install two loggers.
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	defaultLogger.CompareAndSwap(nil, &DefaultLogger{logger: slog.New(handler)})
	return defaultLogger.Load()
}

// Debug logs a debug message
func (l *DefaultLogger) Debug(msg string, args ...any) {
	l.logger.Debug(msg, args...)
}

// Info logs an info message
func (l *DefaultLogger) Info(msg string, args ...any) {
	l.logger.Info(msg, args...)
}

// Warn logs a warning message
func (l *DefaultLogger) Warn(msg string, args ...any) {
	l.logger.Warn(msg, args...)
}

// Error logs an error message
func (l *DefaultLogger) Error(msg string, args ...any) {
	l.logger.Error(msg, args...)
}

// With returns a new logger with the given attributes
func (l *DefaultLogger) With(args ...any) Logger {
	return &DefaultLogger{
		logger: l.logger.With(args...),
	}
}

// Convenience functions for global logger

// Debug logs a debug message using the global logger
func Debug(msg string, args ...any) {
	Get().Debug(msg, args...)
}

// Info logs an info message using the global logger
func Info(msg string, args ...any) {
	Get().Info(msg, args...)
}

// Warn logs a warning message using the global logger
func Warn(msg string, args ...any) {
	Get().Warn(msg, args...)
}

// Error logs an error message using the global logger
func Error(msg string, args ...any) {
	Get().Error(msg, args...)
}
