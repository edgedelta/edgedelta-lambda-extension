package utils

import (
	"sync"
	"sync/atomic"
)

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
