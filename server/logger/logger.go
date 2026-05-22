package logger

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// noopFunc is a reusable no-op function to avoid allocations
var noopFunc = func() {}

// Trace returns a function that logs operation duration when called.
// Returns a no-op function when TRACE level is disabled to avoid overhead.
// Usage: defer logger.Trace("operation")()
func Trace(name string) func() {
	gl := globalLoggerPtr.Load()
	if gl == nil || !gl.shouldLog(LogLevelTrace) {
		return noopFunc
	}
	start := time.Now()
	return func() {
		gl.logWithLevel(LogLevelTrace, "%s: %v", name, time.Since(start))
	}
}

// defaultLogger is used before the global logger is initialized
var defaultLogger = &LimitedLogger{
	file:      os.Stderr,
	lineCount: 0,
	level:     LogLevelInfo,
}

// MaxLogLines defines the maximum number of lines to keep in the log file
const MaxLogLines = 5000

// LogLevel represents the logging level
type LogLevel int

const (
	LogLevelTrace LogLevel = iota
	LogLevelDebug
	LogLevelInfo
	LogLevelWarn
	LogLevelError
)

// String returns the string representation of a log level
func (l LogLevel) String() string {
	switch l {
	case LogLevelTrace:
		return "TRACE"
	case LogLevelDebug:
		return "DEBUG"
	case LogLevelInfo:
		return "INFO"
	case LogLevelWarn:
		return "WARN"
	case LogLevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// ParseLogLevel parses a string into a LogLevel
func ParseLogLevel(s string) LogLevel {
	switch strings.ToUpper(s) {
	case "TRACE":
		return LogLevelTrace
	case "DEBUG":
		return LogLevelDebug
	case "INFO":
		return LogLevelInfo
	case "WARN", "WARNING":
		return LogLevelWarn
	case "ERROR":
		return LogLevelError
	default:
		return LogLevelInfo
	}
}

// LimitedLogger wraps the standard log.Logger with line count limiting and log levels
type LimitedLogger struct {
	file      *os.File
	lineCount int
	level     LogLevel
	pid       int
	mutex     sync.Mutex
}

// Global logger instance (atomic for safe concurrent access)
var globalLoggerPtr atomic.Pointer[LimitedLogger]

// NewLimitedLogger creates a new LimitedLogger
func NewLimitedLogger(file *os.File, level LogLevel) *LimitedLogger {
	ll := &LimitedLogger{
		file:      file,
		lineCount: 0,
		level:     level,
		pid:       os.Getpid(),
	}

	// Count existing lines in the file
	ll.countExistingLines()
	globalLoggerPtr.Store(ll)
	return ll
}

// shouldLog returns true if the given level should be logged
func (ll *LimitedLogger) shouldLog(level LogLevel) bool {
	return level >= ll.level
}

// logWithLevel logs a message at the specified level
func (ll *LimitedLogger) logWithLevel(level LogLevel, format string, v ...any) {
	if !ll.shouldLog(level) {
		return
	}
	// Format with timestamp and write through Write() for proper line counting/rotation
	msg := fmt.Sprintf("%s [%s] [PID %d] %s\n", time.Now().Format("2006/01/02 15:04:05"), level.String(), ll.pid, fmt.Sprintf(format, v...))
	ll.Write([]byte(msg))
}

// Debug logs a debug message
func (ll *LimitedLogger) Debug(format string, v ...any) {
	ll.logWithLevel(LogLevelDebug, format, v...)
}

// Info logs an info message
func (ll *LimitedLogger) Info(format string, v ...any) {
	ll.logWithLevel(LogLevelInfo, format, v...)
}

// Warn logs a warning message
func (ll *LimitedLogger) Warn(format string, v ...any) {
	ll.logWithLevel(LogLevelWarn, format, v...)
}

// Error logs an error message
func (ll *LimitedLogger) Error(format string, v ...any) {
	ll.logWithLevel(LogLevelError, format, v...)
}

// Fatal logs an error message and exits with code 1
func (ll *LimitedLogger) Fatal(format string, v ...any) {
	ll.logWithLevel(LogLevelError, format, v...)
	os.Exit(1)
}

// getLogger returns the global logger or the default fallback.
func getLogger() *LimitedLogger {
	if gl := globalLoggerPtr.Load(); gl != nil {
		return gl
	}
	return defaultLogger
}

// SetLevel changes the log level of the active logger.
func SetLevel(level LogLevel) {
	getLogger().level = level
}

// Package-level logging functions that use the global logger (or default if not initialized)
func Debug(format string, v ...any) { getLogger().Debug(format, v...) }
func Info(format string, v ...any)  { getLogger().Info(format, v...) }
func Warn(format string, v ...any)  { getLogger().Warn(format, v...) }
func Error(format string, v ...any) { getLogger().Error(format, v...) }
func Fatal(format string, v ...any) { getLogger().Fatal(format, v...) }

// countExistingLines counts the number of lines in the current log file
func (ll *LimitedLogger) countExistingLines() {
	ll.mutex.Lock()
	defer ll.mutex.Unlock()

	// Seek to beginning of file
	ll.file.Seek(0, 0)
	scanner := bufio.NewScanner(ll.file)

	count := 0
	for scanner.Scan() {
		count++
	}

	ll.lineCount = count

	// Seek back to end of file for appending
	ll.file.Seek(0, 2)
}

// Write implements io.Writer interface
func (ll *LimitedLogger) Write(p []byte) (n int, err error) {
	ll.mutex.Lock()
	defer ll.mutex.Unlock()

	// Write to file
	n, err = ll.file.Write(p)
	if err != nil {
		return n, err
	}

	// Count newlines in the written data
	newlines := strings.Count(string(p), "\n")
	ll.lineCount += newlines

	// Check if we need to rotate the log file
	if ll.lineCount > MaxLogLines {
		ll.rotateLogFile()
	}

	return n, err
}

// rotateLogFile trims the log file to keep only the last MaxLogLines/2 lines.
// Scans bytes from the end to find the cut point, avoiding loading all lines into memory.
func (ll *LimitedLogger) rotateLogFile() {
	keepLines := MaxLogLines / 2

	// Get file size
	size, err := ll.file.Seek(0, io.SeekEnd)
	if err != nil || size == 0 {
		return
	}

	// Scan backwards from end to find the byte offset of the keepLines-th newline
	buf := make([]byte, min(int(size), 64*1024))
	newlineCount := 0
	cutOffset := int64(0)

	for pos := size; pos > 0; {
		readSize := min(int64(len(buf)), pos)
		pos -= readSize
		ll.file.Seek(pos, io.SeekStart)
		n, err := ll.file.Read(buf[:readSize])
		if err != nil || n == 0 {
			break
		}
		for i := n - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				newlineCount++
				if newlineCount == keepLines {
					cutOffset = pos + int64(i) + 1
					goto found
				}
			}
		}
	}

found:
	// Read content from cut point to end
	ll.file.Seek(cutOffset, io.SeekStart)
	kept, err := io.ReadAll(ll.file)
	if err != nil {
		return
	}

	// Truncate and rewrite
	ll.file.Truncate(0)
	ll.file.Seek(0, io.SeekStart)
	ll.file.Write(kept)

	ll.lineCount = keepLines
}

// Close closes the underlying file
func (ll *LimitedLogger) Close() error {
	return ll.file.Close()
}
