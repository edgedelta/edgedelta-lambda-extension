package utils

import (
	"bytes"
	"context"
	"log"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff"
	"golang.org/x/sys/unix"
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

// Pool is a worker pool which only allows limited number of goroutines to execute concurrently.
type Pool struct {
	limit         int
	wg            sync.WaitGroup
	activeWorkers chan struct{}
	inProgress    int32
}

func NewWorkerPool(limit int) *Pool {
	return &Pool{
		limit:         limit,
		activeWorkers: make(chan struct{}, limit),
	}
}

// Execute creates a new goroutine which invokes given function as soon as there's an open slot.
func (p *Pool) Execute(fn func()) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.enter()
		fn()
		p.exit()
	}()
}

// Wait blocks until all workers finish their hjob
func (p *Pool) Wait() {
	p.wg.Wait()
}

// InProgress returns number of inprogress jobs.
func (p *Pool) InProgress() int32 {
	return atomic.LoadInt32(&p.inProgress)
}

func (p *Pool) enter() {
	p.activeWorkers <- struct{}{} // enter the active area
	atomic.AddInt32(&p.inProgress, 1)
}

func (p *Pool) exit() {
	atomic.AddInt32(&p.inProgress, -1)
	<-p.activeWorkers // exit the active area
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

func CopyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	copied := make(map[string]string, len(m))
	for k, v := range m {
		copied[k] = v
	}
	return copied
}
