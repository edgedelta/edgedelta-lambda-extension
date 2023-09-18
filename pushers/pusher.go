package pushers

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/edgedelta/edgedelta-lambda-extension/cfg"
	"github.com/edgedelta/edgedelta-lambda-extension/utils"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/firehose"
)

const flushTimeout = 5 * time.Second

var (
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

type invocation struct {
	Ctx   context.Context
	DoneC chan struct{}
}

type Pusher struct {
	name              string
	logClient         *http.Client
	inC               chan []byte
	invokeC           chan *invocation
	runtimeDoneC      chan struct{}
	stopC             chan time.Duration
	stoppedC          chan struct{}
	kinesisClient     *firehose.Firehose
	makeRequestFunc   func(context.Context, []byte) error
	endpoint          string
	flushAtNextInvoke bool
	bufferSize        int
	retryInterval     time.Duration
	pushTimeout       time.Duration
}

// NewPusher initializes Pusher
func NewPusher(conf *cfg.Config, inC chan []byte, runtimeDoneC chan struct{}) *Pusher {
	p := &Pusher{
		inC:               inC,
		runtimeDoneC:      runtimeDoneC,
		stopC:             make(chan time.Duration),
		stoppedC:          make(chan struct{}),
		invokeC:           make(chan *invocation),
		flushAtNextInvoke: conf.FlushAtNextInvoke,
		retryInterval:     conf.RetryInterval,
		pushTimeout:       conf.PushTimeout,
		bufferSize:        conf.BufferSize,
	}
	// default mode is http
	switch conf.PusherMode {
	case cfg.KINESIS_PUSHER:
		p.name = "Kinesis-Pusher"
		sess := session.Must(session.NewSession())
		p.kinesisClient = firehose.New(sess)
		p.makeRequestFunc = p.makeKinesisRequest
		p.endpoint = conf.KinesisEndpoint
	case cfg.HTTP_PUSHER:
		p.name = "HostedEnv-Pusher"
		p.logClient = newHTTPClientFunc()
		p.makeRequestFunc = p.makeHTTPRequest
		p.endpoint = conf.EDEndpoint
	}
	return p
}

func (p *Pusher) Start() {
	log.Printf("Starting %s", p.name)
	utils.Go("Pusher.run", func() {
		p.run()
	})
}

func (p *Pusher) Invoke(ctx context.Context, doneC chan struct{}) {
	log.Printf("Invoking pusher")
	p.invokeC <- &invocation{Ctx: ctx, DoneC: doneC}
	log.Printf("Invoked pusher")
}

func (p *Pusher) Stop(timeout time.Duration) {
	log.Printf("Stopping pusher with timeout %v", timeout)
	p.stopC <- timeout
	<-p.stoppedC
	log.Printf("Pusher stopped")
}

func (p *Pusher) run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var doneC chan struct{}
	buf := new(bytes.Buffer)
	flushRespC := make(chan []byte)
	for {
		select {
		case b := <-p.inC:
			buf.Write(b)
		case <-p.runtimeDoneC:
			if !p.flushAtNextInvoke {
				payload := buf.Bytes()
				utils.Go("Pusher.flush", func() {
					p.flush(ctx, payload, flushRespC)
				})
				buf = new(bytes.Buffer)
			}
		case inv := <-p.invokeC:
			ctx = inv.Ctx
			doneC = inv.DoneC
			if p.flushAtNextInvoke {
				if buf.Len() == 0 {
					doneC <- struct{}{}
					doneC = nil
				} else {
					payload := buf.Bytes()
					utils.Go("Pusher.flush", func() {
						p.flush(ctx, payload, flushRespC)
					})
					buf = new(bytes.Buffer)
				}
			}
		case failedPayload := <-flushRespC:
			if len(failedPayload) > 0 {
				newBuf := new(bytes.Buffer)
				newBuf.Write(failedPayload)
				newBuf.Write(buf.Bytes())
				buf = newBuf
			}
			if doneC != nil {
				doneC <- struct{}{}
			}
			doneC = nil
		case timeout := <-p.stopC:
			if buf.Len() > 0 {
				log.Print("Flushing logs")
				// Blocking
				if err := p.push(context.Background(), buf.Bytes(), timeout); err != nil {
					log.Printf("Failed to flush logs, err: %v", err)
				} else {
					log.Print("Flush completed")
				}
			}
			if doneC != nil {
				doneC <- struct{}{}
			}
			p.stoppedC <- struct{}{}
			return
		}
	}
}

func (p *Pusher) push(ctx context.Context, payload []byte, timeout time.Duration) error {
	return utils.DoWithExpBackoffC(ctx, func() error {
		reqCtx, cancel := context.WithTimeout(ctx, p.pushTimeout)
		defer cancel()
		return p.makeRequestFunc(reqCtx, payload)
	}, p.retryInterval, timeout)
}

func (p *Pusher) flush(ctx context.Context, payload []byte, flushRespC chan []byte) {
	log.Print("Flushing logs")
	err := p.push(ctx, payload, flushTimeout)
	if err != nil {
		log.Printf("Failed to flush logs, err: %v", err)
		if len(payload) > p.bufferSize {
			log.Printf("Dropping logs")
			start := p.bufferSize / 10
			for i := start; i < len(payload); i++ {
				if payload[i] == sep {
					start = i + 1
					break
				}
			}
			payload = payload[start:] // Drop 1/10 of logs
		}
		flushRespC <- payload
		return
	}
	log.Print("Flush completed")
	flushRespC <- nil
}

func (p *Pusher) makeHTTPRequest(ctx context.Context, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create http post request: %s, err: %v", p.endpoint, err)
	}
	req.Close = true
	req.Header.Add("Content-Type", "application/json")
	return p.sendWithCaringResponseCode(req)
}

func (p *Pusher) makeKinesisRequest(ctx context.Context, payload []byte) error {
	record := &firehose.Record{Data: payload}
	_, err := p.kinesisClient.PutRecordWithContext(ctx, &firehose.PutRecordInput{
		DeliveryStreamName: aws.String(p.endpoint),
		Record:             record,
	})
	return err
}

func (p *Pusher) sendWithCaringResponseCode(req *http.Request) error {
	resp, err := p.logClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("%s response body read failed err: %v", p.name, err)
		}
		body := string(bodyBytes)
		if body != "" {
			s := fmt.Sprintf("%s returned unexpected status code: %v response: %s", p.name, resp.StatusCode, body)
			return fmt.Errorf(s, body)
		}
		return fmt.Errorf("%s returned unexpected status code: %v", p.name, resp.StatusCode)
	}

	return nil
}
