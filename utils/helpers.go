package utils

import (
	"bytes"
	"context"
	"log"
	"runtime/debug"
	"time"

	"github.com/cenkalti/backoff"
	"golang.org/x/sys/unix"
)

var (
	defaultRandomizationFactor = 0.5
)

// DoWithExpBackoffC invokes given func f until it doesn't return error or timeout is reached.
// f will wait for exponential-backoff intervals between retries.
// e.g. for initial interval 0.5s it will follow backoffs: 0.5, 0.75, 1.125, 1.687, 2.53..
// randomization is enabled by default so the backoff intervals are not going to be exact.
// It takes a context parameter and respects ctx.Done() signal.
func DoWithExpBackoffC(ctx context.Context, f func() error, initialInterval, timeout time.Duration) error {
	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.RandomizationFactor = defaultRandomizationFactor
	expBackoff.MaxElapsedTime = timeout
	expBackoff.InitialInterval = initialInterval
	b := backoff.WithContext(expBackoff, ctx)
	return backoff.Retry(f, b)
}

// Go runs the given func in a goroutine which will catch & log if it panics
// Related discussion: https://github.com/golang/go/issues/20161
func Go(name string, fn func()) {
	go func() {
		defer func() {
			if err := recover(); err != nil {
				log.Panicf("%s panicked with err: %v, stack trace:\n%s", name, err, debug.Stack())
			}
		}()
		fn()
	}()
}

const (
	X86Architecture = "x86_64"
	ArmArchitecture = "arm64"
	AmdArchitecture = "amd64"
)

func GetRuntimeArchitecture() string {
	var uname unix.Utsname
	if err := unix.Uname(&uname); err != nil {
		return AmdArchitecture
	}

	switch string(uname.Machine[:bytes.IndexByte(uname.Machine[:], 0)]) {
	case "aarch64":
		return ArmArchitecture
	default:
		return X86Architecture
	}
}

func GetPointerIfNotDefaultValue[T comparable](v T) *T {
	var defaultValue T
	if v == defaultValue {
		return nil
	}

	return &v
}
