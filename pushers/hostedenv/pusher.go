package hostedenv

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edgedelta/edgedelta-lambda-extension/cfg"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/host"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/httpext"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/log"
	"github.com/edgedelta/edgedelta-lambda-extension/pushers"
)

var (
	hostNameFunc      = func() string { return host.Name }
	newHTTPClientFunc = func() *http.Client {
		t := &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
				DualStack: true,
			}).DialContext,
			// MaxIdleConnsPerHost does not work as expected
			// https://github.com/golang/go/issues/13801
			// https://github.com/OJ/gobuster/issues/127
			// Improve connection re-use
			MaxIdleConns: 256,
			// Observed rare 1 in 100k connection reset by peer error with high number MaxIdleConnsPerHost
			// Most likely due to concurrent connection limit from server side per host
			// https://edgedelta.atlassian.net/browse/ED-663
			MaxIdleConnsPerHost:   128,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		}
		return &http.Client{Transport: t}
	}
)

type Pusher struct {
	host      string
	name      string
	streamer  *pushers.Streamer
	logClient *httpext.Client
	conf      *cfg.Config
	isRunning int32
}

// NewPusher initialize hostedenv pusher.
func NewPusher(conf *cfg.Config) (*Pusher, error) {
	o := []httpext.Option{
		httpext.WithRetry(
			20*time.Millisecond,
			15*time.Second,
			45*time.Second,
			httpext.DefaultRetryIf,
		),
	}
	p := &Pusher{
		conf:      conf,
		host:      hostNameFunc(),
		name:      "HostedEnv-Pusher",
		logClient: httpext.NewClient([]string{"HostedEnv"}, newHTTPClientFunc(), o...),
	}

	atomic.StoreInt32(&p.isRunning, 1)
	p.streamer = pushers.NewStreamer("HostedEnv-Streamer", conf.BufferSize, conf.Parallelism)
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
		p.logClient.Stop()
		log.Debug("%s stopped", p.name)
	}
}

func (p *Pusher) post(ctx context.Context, payload []byte) error {
	if payload == nil {
		return fmt.Errorf("%s post is called with nil data", p.name)
	}

	req, err := http.NewRequest(http.MethodPost, p.conf.EDEndpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create http post request: %s, err: %v", p.conf.EDEndpoint, err)
	}
	req.Close = true
	req.Header.Add("Content-Type", "application/json")
	return httpext.SendWithCaringResponseCode(p.logClient, req, p.name)
}
