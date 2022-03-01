package utils

import (
	"context"
	"log"
	"runtime/debug"
	"time"

	"github.com/cenkalti/backoff"
)

var (
	defaultRandomizationFactor = 0.5
)

// Go runs the given func in a goroutine which will catch & log if it panics
// Related discussion: https://github.com/golang/go/issues/20161
func Go(name string, fn func()) {
	go func() {
		defer func() {
			if err := recover(); err != nil {
				log.Panicf("%s paniced with err: %v, stack trace:\n%s", name, err, debug.Stack())
			}
		}()
		fn()
	}()
}

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
