package log

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"runtime/pprof"
	"strings"
	"sync"
	"time"

	"github.com/edgedelta/edgedelta-lambda-extension/pkg/env"
)

// Level defines Log Level
type Level int8

const (
	// DebugLevel logs produce a large size of logs so should be avoided in Production
	// in Development should be fine to use
	DebugLevel Level = iota
	// InfoLevel for Production environment
	InfoLevel
	// WarnLevel sth happened or received out of expectation
	WarnLevel
	// ErrorLevel for high-priority cases but program can still run
	ErrorLevel
	// PanicLevel logs a message, then panics
	PanicLevel
	// FatalLevel for crucial cases means program is not able run any more
	// after logging message then calls os.Exit(1)
	FatalLevel
)

var (
	mu sync.Mutex
	// Note that we have a dependency on the format of "ERROR" messages in the log
	// in the integration tests. So, if we change the logger here, the corresponding
	// entry in the integration tests needs to be changed.
	logger       = newZapLogger(os.Stdout)
	level  Level = InfoLevel

	levelMap = map[string]Level{
		"debug": DebugLevel,
		"info":  InfoLevel,
		"warn":  WarnLevel,
		"error": ErrorLevel,
		"fatal": FatalLevel,
	}

	// LevelNamesMap is the mappoing between log level to string representation
	LevelNamesMap = map[Level]string{
		DebugLevel: "debug",
		InfoLevel:  "info",
		WarnLevel:  "warn",
		ErrorLevel: "error",
		FatalLevel: "fatal",
	}

	// debugMap stores the set of messages that are suppressed.
	// TODO: add ttl support so it doesn't grow infinitely.
	debugMap = &sync.Map{}

	dumpGoroutinesOnFatal = true

	// RecordSelfLog is noop by default. It can be overridden to collect agent logs.
	RecordSelfLog = NoOpRecordSelfLog
	// FlushSelfLogs is noop by default. It must be overridden together with RecordSelfLog to collect agent logs.
	FlushSelfLogs = NoOpFlushSelfLogs
)

// SetLevel to change the log level in run-time
func SetLevel(l Level) {
	mu.Lock()
	defer mu.Unlock()
	level = l
}

// DebugOn returns true if log current level is equal or lower than Debug currently min log level is Debug.
// In some conditions, creating debug string is also an expensive procedure, this flag can be used to eliminate such evaluation.
// if log.DebugOn() {
//     message := expensiveString()
//     log.Debug("My message %s", message)
// }
func DebugOn() bool {
	return level <= DebugLevel
}

// SetLevelWithName to change the log level with level name in run-time
// it works case-insensitive for levelName, if levelName is invalid
// then level is assigned to InfoLevel
func SetLevelWithName(levelName string) {
	mu.Lock()
	defer mu.Unlock()
	l, ok := levelMap[strings.ToLower(levelName)]
	if !ok {
		level = InfoLevel
	} else {
		level = l
	}
}

// DisableGoroutineDumpOnFatal disables the goroutine dump on fatal logs.
// It's not needed to print goroutines in some scenarios such as intentionally exiting the process in tools.
func DisableGoroutineDumpOnFatal() {
	mu.Lock()
	defer mu.Unlock()
	dumpGoroutinesOnFatal = false
}

func message(format string, args ...interface{}) string {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	return msg
}

// Debug logs a message at DebugLevel. First parameter is a string to use
// formatting the message and it takes any number of arguments to build the log message.
// If you have just a log message without format then
// use just one parameter to build log message and logs it
func Debug(format string, args ...interface{}) {
	if level <= DebugLevel {
		msg := message(format, args...)
		logger.Debug(msg)
		RecordSelfLog(msg, DebugLevel)
	}
}

// DebugOnce logs the message at DebugLevel and does it only once.
// Useful for suppressing logs from noisy sources which periodically prints same thing over and over.
// Don't call DebugOnce for logs which vary in text e.g. log.DebugOnce("%s", time.Now()).
// The formatted message hash is kept in memory for future to check whether it was logged before.
func DebugOnce(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if _, loaded := debugMap.LoadOrStore(message, true); !loaded {
		if level <= DebugLevel {
			logger.Debug(message)
			RecordSelfLog(message, DebugLevel)
		}
	}
}

// Info logs a message at InfoLevel. First parameter is a string to use
// formatting the message and it takes any number of arguments to build the log message.
// If you have just a log message without format then
// use just one parameter to build log message and logs it
func Info(format string, args ...interface{}) {
	if level <= InfoLevel {
		msg := message(format, args...)
		logger.Info(msg)
		RecordSelfLog(msg, InfoLevel)
	}
}

// Warn logs a message at WarnLevel. First parameter is a string to use
// formatting the message and it takes any number of arguments to build the log message.
// If you have just a log message without format then
// use just one parameter to build log message and logs it
func Warn(format string, args ...interface{}) {
	if level <= WarnLevel {
		msg := message(format, args...)
		logger.Warn(msg)
		RecordSelfLog(msg, WarnLevel)
	}
}

// Error logs a message at ErrorLevel. First parameter is a string to use
// formatting the message and it takes any number of arguments to build the log message.
// If you have just a log message without format then
// use just one parameter to build log message and logs it
func Error(format string, args ...interface{}) {
	if level <= ErrorLevel {
		msg := message(format, args...)
		logger.Error(msg)
		RecordSelfLog(msg, ErrorLevel)
	}
}

// Panic logs a message at DebugLevel then the logger then panics.
// First parameter is a string to use formatting the message
// and it takes any number of arguments to build the log message.
// If you have just a log message without format then
// use just one parameter to build log message and logs it
func Panic(format string, args ...interface{}) {
	if level <= PanicLevel {
		msg := message(format, args...)
		RecordSelfLog(msg, FatalLevel)
		FlushSelfLogs()
		logger.Panic(msg)
	}
}

// Fatal logs a message at FatalLevel then calls os.Exit(1)
// First parameter is a string to use formatting the message
// and it takes any number of arguments to build the log message.
// If you have just a log message without format then
// use just one parameter to build log message and logs it
func Fatal(format string, args ...interface{}) {
	if level <= FatalLevel {
		if dumpGoroutinesOnFatal {
			msg := message("running goroutines: %s", runningGoRoutines())
			logger.Info(msg)
			RecordSelfLog(msg, InfoLevel)
		}
		msg := message(format, args...)
		RecordSelfLog(msg, FatalLevel)

		// let self log uploader consume the fatal message before flushing
		dur := 100 * time.Millisecond
		if durEnv := env.Get("ED_FATAL_SELF_LOG_FLUSH_SLEEP"); durEnv != "" {
			if val, err := time.ParseDuration(durEnv); err == nil {
				dur = val
			}
		}
		time.Sleep(dur)
		FlushSelfLogs()

		// finally log the fatal log which will terminate the process
		logger.Fatal(msg)
	}
}

// DebugC implements same functionality as Info with context
func DebugC(ctx context.Context, format string, args ...interface{}) {
	if level <= DebugLevel {
		fields := transformFunc(ctx)
		msg := message(format, args...)
		logger.Debug(msg, toZap(fields)...)
		RecordSelfLog(msg, DebugLevel)
	}
}

// InfoC implements same functionality as Info with context
func InfoC(ctx context.Context, format string, args ...interface{}) {
	if level <= InfoLevel {
		fields := transformFunc(ctx)
		msg := message(format, args...)
		logger.Info(msg, toZap(fields)...)
		RecordSelfLog(msg, InfoLevel)
	}
}

// WarnC implements same functionality as Info with context
func WarnC(ctx context.Context, format string, args ...interface{}) {
	if level <= WarnLevel {
		fields := transformFunc(ctx)
		msg := message(format, args...)
		logger.Warn(msg, toZap(fields)...)
		RecordSelfLog(msg, WarnLevel)
	}
}

// ErrorC implements same functionality as Info with context
func ErrorC(ctx context.Context, format string, args ...interface{}) {
	if level <= ErrorLevel {
		fields := transformFunc(ctx)
		msg := message(format, args...)
		logger.Error(msg, toZap(fields)...)
		RecordSelfLog(msg, ErrorLevel)
	}
}

// PanicC implements same functionality as Info with context
func PanicC(ctx context.Context, format string, args ...interface{}) {
	if level <= PanicLevel {
		fields := transformFunc(ctx)
		msg := message(format, args...)
		RecordSelfLog(msg, FatalLevel)
		FlushSelfLogs()
		logger.Panic(msg, toZap(fields)...)
	}
}

// FatalC implements same functionality as Info with context
func FatalC(ctx context.Context, format string, args ...interface{}) {
	if level <= FatalLevel {
		fields := transformFunc(ctx)
		if dumpGoroutinesOnFatal {
			msg := message("running goroutines: %s", runningGoRoutines())
			logger.Info(msg, toZap(fields)...)
			RecordSelfLog(msg, InfoLevel)
		}
		msg := message(format, args...)
		RecordSelfLog(msg, FatalLevel)
		FlushSelfLogs()
		logger.Fatal(msg, toZap(fields)...)
	}
}

// NoOpRecordSelfLog .
func NoOpRecordSelfLog(string, Level) {}

// NoOpFlushSelfLogs .
func NoOpFlushSelfLogs() {}

func runningGoRoutines() string {
	var buf bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
	return buf.String()
}
