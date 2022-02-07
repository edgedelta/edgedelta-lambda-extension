package s3

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edgedelta/edgedelta-lambda-extension/cfg"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/aws/session"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/aws/uploader"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/host"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/log"
	"github.com/edgedelta/edgedelta-lambda-extension/pushers"
	"github.com/google/uuid"
)

var (
	hostNameFunc = func() string { return host.Name }

	newUploadRepo = func(s session.Settings) (uploadRepository, error) {
		return createUploadRepo(s)
	}
)

type uploadRepository interface {
	Upload(ctx context.Context, input *uploader.Input) error
	Stop()
}

type Pusher struct {
	host       string
	name       string
	fileSuffix string
	streamer   *pushers.Streamer
	uploadRepo uploadRepository
	conf       *cfg.Config
	isRunning  int32
}

// NewPusher initialize S3 pusher.
func NewPusher(conf *cfg.Config) (*Pusher, error) {
	// empty settings use default shared credentials and us-west-2 region for aws operations.
	setting := session.Settings{}

	repo, err := newUploadRepo(setting)
	if err != nil {
		return nil, err
	}

	p := &Pusher{
		conf:       conf,
		uploadRepo: repo,
		host:       hostNameFunc(),
		name:       fmt.Sprintf("%s-Pusher", conf.S3BucketName),
		fileSuffix: "log",
	}

	atomic.StoreInt32(&p.isRunning, 1)
	p.streamer = pushers.NewStreamer("S3-Streamer", conf.BufferSize, conf.Parallelism)
	return p, nil
}

// Push submits given data to streamer queue.
func (p *Pusher) Push(data []byte) {
	if len(data) == 0 {
		return
	}
	p.streamer.Push(&pushers.Item{
		Data: data,
		Do:   p.post,
	})
}

// FlushLogs activates streamer to consume logs.
func (p *Pusher) FlushLogs(isShutdown bool, wg *sync.WaitGroup) {
	defer wg.Done()
	p.streamer.Start(isShutdown)
}

// Stop S3 pusher.
func (p *Pusher) Stop() {
	if atomic.CompareAndSwapInt32(&p.isRunning, 1, 0) {
		p.streamer.Stop()
		if p.uploadRepo != nil {
			p.uploadRepo.Stop()
		}
		log.Debug("%s stopped", p.name)
	}
}

func (p *Pusher) post(ctx context.Context, payload []byte) error {
	if payload == nil {
		return fmt.Errorf("%s post is called with nil data", p.name)
	}
	var buffer *bytes.Buffer
	fileName := uuid.New().String()
	buffer = bytes.NewBuffer(payload)
	input := &uploader.Input{
		Key:        fmt.Sprintf("%s/%s/%s.%s", time.Now().UTC().Format("2006/01/02/15"), p.host, fileName, p.fileSuffix),
		Bucket:     p.conf.S3BucketName,
		Data:       buffer,
		Compressed: false,
	}

	return p.uploadRepo.Upload(ctx, input)
}

func createUploadRepo(s session.Settings) (uploadRepository, error) {
	cl, err := uploader.NewClient(uploader.WithSessionSettings(s))
	if err != nil {
		return nil, err
	}
	return cl, nil
}
