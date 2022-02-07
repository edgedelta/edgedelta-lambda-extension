package pushers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/edgedelta/edgedelta-lambda-extension/lambda"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/log"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/utils"
)

type Streamer struct {
	name            string
	queue           chan *Item
	stop            chan struct{}
	stopped         chan struct{}
	parallelization int
}

// Item encapsulates a payload, the function that pushes it to a streaming endpoint and
// other related configuration such as retry limits
type Item struct {
	// Data can be anything. Do function knows what to do with it
	Data []byte

	// Do function will be invoked with given data. It returns error if any.
	// If it returns error it might be invoked again based on retry configuration
	Do func(context.Context, []byte) error

	// RetryInterval is the initial interval to wait until next retry. It is increased exponentially until timeout limit is reached.
	// Some Do implementations might have their own custom retry so this one is optional.
	// When RetryInterval is not set Do is not retried at all.
	RetryInterval time.Duration

	// TimeoutLimit is the total duration for which to keep retry.
	TimeoutLimit time.Duration
}

func NewStreamer(name string, buffer, parallelization int) *Streamer {
	return &Streamer{
		name:            name,
		queue:           make(chan *Item, buffer),
		stop:            make(chan struct{}),
		stopped:         make(chan struct{}),
		parallelization: parallelization,
	}
}

// Start starts the streamer
func (s *Streamer) Start(isShutdown bool) {
	wg := new(sync.WaitGroup)
	for i := 0; i < s.parallelization; i++ {
		i := i
		wg.Add(1)
		utils.Go(fmt.Sprintf("%s.run#%d", s.name, i), func() { s.run(i, wg, isShutdown) })
	}
	wg.Wait()
}

// Stop stops the streamer
func (s *Streamer) Stop() {
	// stop streaming goroutines
	for i := 0; i < s.parallelization; i++ {
		s.stop <- struct{}{}
		<-s.stopped
	}
	log.Debug("%s stopped", s.name)
}

// Push pushes the given item to internal queue to be picked up by workers
func (s *Streamer) Push(i *Item) {
	s.queue <- i
}

func (s *Streamer) run(id int, wg *sync.WaitGroup, isShutdown bool) {
	defer wg.Done()
	log.Debug("%s goroutine %d started running", s.name, id)
	ctx, cancel := context.WithCancel(context.Background())
	var checkRuntimeStr string
	// we need to wait until either lambda runtime is done or shutdown event received and flushing the queue.
	for !(len(s.queue) == 0 && (isShutdown || strings.Contains(checkRuntimeStr, string(lambda.RuntimeDone)))) {
		select {
		case item := <-s.queue:
			checkRuntimeStr = string(item.Data)
			err := s.do(ctx, item)
			if err != nil {
				log.Debug("Error streaming data from %s, err: %v", s.name, err)
			}
		case <-s.stop:
			cancel()
			s.stopped <- struct{}{}
			log.Debug("%s goroutine %d stopped", s.name, id)
			return
		}
	}
	cancel()
	log.Debug("%s goroutine %d drained its queue or received runtime done, stopped", s.name, id)
	// we need to stop all other listening goroutines also
	s.runtimeDone()
}

func (s *Streamer) do(ctx context.Context, item *Item) error {
	var err error
	if item.RetryInterval > 0 {
		err = utils.DoWithExpBackoffC(ctx, func() error {
			err = item.Do(ctx, item.Data)
			return err
		}, item.RetryInterval, item.TimeoutLimit)
	} else {
		err = item.Do(ctx, item.Data)
	}
	return err
}

func (s *Streamer) runtimeDone() {
	// stop streaming goroutines
	for i := 0; i < s.parallelization-1; i++ {
		s.stop <- struct{}{}
		<-s.stopped
	}
	log.Debug("%s runtime done", s.name)
}
