package pushers

import (
	"sync"
)

type Pusher interface {
	Push([]byte)
	Stop()
	FlushLogs(bool, *sync.WaitGroup)
}

type MultiPusher struct {
	pushers []Pusher
}

// NewMultiPusher creates a new multiple destination pusher.
func NewMultiPusher(pushers []Pusher) *MultiPusher {
	return &MultiPusher{
		pushers: pushers,
	}
}

// Push sends data to each underlying pusher.
func (m *MultiPusher) Push(data []byte) {
	for _, p := range m.pushers {
		go p.Push(data)
	}
}

func (m *MultiPusher) FlushLogs(isShutdown bool) {
	wg := new(sync.WaitGroup)
	for _, p := range m.pushers {
		wg.Add(1)
		go p.FlushLogs(isShutdown, wg)
	}
	wg.Wait()
}

// Stop stops underlying pushers.
func (m *MultiPusher) Stop() {
	for _, p := range m.pushers {
		p.Stop()
	}
}
