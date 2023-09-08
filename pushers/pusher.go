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

// Start dropping logs after payloads exceed maxNumberOfPayloadsToKeep
// Logs might be dropped if endpoint cannot be reached

const maxNumberOfPayloadsToKeep = 10
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

type StopPayload struct {
	Timeout time.Duration
	Buffer  *bytes.Buffer
}

type Pusher struct {
	name              string
	logClient         *http.Client
	inC               chan *bytes.Buffer
	invokeC           chan *invocation
	pusherStopC       chan *StopPayload
	stoppedC          chan struct{}
	kinesisClient     *firehose.Firehose
	makeRequestFunc   func(context.Context, []*bytes.Buffer) error
	endpoint          string
	flushAtNextInvoke bool
	retryInterval     time.Duration
	pushTimeout       time.Duration
}

// NewPusher initializes Pusher
func NewPusher(conf *cfg.Config, inC chan *bytes.Buffer, pusherStopC chan *StopPayload) *Pusher {
	p := &Pusher{
		inC:               inC,
		pusherStopC:       pusherStopC,
		stoppedC:          make(chan struct{}),
		invokeC:           make(chan *invocation),
		retryInterval:     conf.RetryInterval,
		pushTimeout:       conf.PushTimeout,
		flushAtNextInvoke: conf.FlushAtNextInvoke,
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

func (p *Pusher) Stop() {
	<-p.stoppedC
	log.Printf("Pusher stopped")
}

func (p *Pusher) run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var doneC chan struct{}
	var payloads []*bytes.Buffer
	flushRespC := make(chan []*bytes.Buffer)
	for {
		select {
		case r := <-p.inC:
			if r.Len() > 0 {
				payloads = append(payloads, r)
			}
			if len(payloads) > 0 && !p.flushAtNextInvoke {
				pushPayloads := payloads
				utils.Go("Pusher.flush", func() {
					p.flush(ctx, pushPayloads, flushRespC)
				})
				payloads = nil
			}
		case inv := <-p.invokeC:
			ctx = inv.Ctx
			doneC = inv.DoneC
			if len(payloads) > 0 && p.flushAtNextInvoke {
				pushPayloads := payloads
				utils.Go("Pusher.flush", func() {
					p.flush(ctx, pushPayloads, flushRespC)
				})
				payloads = nil
			}
		case flushResp := <-flushRespC:
			payloads = append(flushResp, payloads...)
			if doneC != nil {
				doneC <- struct{}{}
			}
			doneC = nil
		case stop := <-p.pusherStopC:
			log.Print("Stopping pusher")
			if stop.Buffer.Len() > 0 {
				payloads = append(payloads, stop.Buffer)
			}
			if len(payloads) > 0 {
				log.Print("Flushing logs")
				// Blocking
				if err := p.push(context.Background(), payloads, stop.Timeout); err != nil {
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

func (p *Pusher) push(ctx context.Context, payloads []*bytes.Buffer, timeout time.Duration) error {
	return utils.DoWithExpBackoffC(ctx, func() error {
		reqCtx, cancel := context.WithTimeout(ctx, p.pushTimeout)
		defer cancel()
		return p.makeRequestFunc(reqCtx, payloads)
	}, p.retryInterval, timeout)
}

func (p *Pusher) flush(ctx context.Context, payloads []*bytes.Buffer, flushRespC chan []*bytes.Buffer) {
	log.Print("Flushing logs")
	err := p.push(ctx, payloads, flushTimeout)
	if err != nil {
		log.Printf("Failed to flush logs, err: %v", err)
		if len(payloads) > maxNumberOfPayloadsToKeep {
			log.Printf("Dropping logs")
			payloads = payloads[1:]
		}
		flushRespC <- payloads
		return
	}
	log.Print("Flush completed")
	flushRespC <- nil
}

func (p *Pusher) makeHTTPRequest(ctx context.Context, payloads []*bytes.Buffer) error {
	readers := make([]io.Reader, len(payloads))
	for i := 0; i < len(payloads); i++ {
		readers[i] = payloads[i]
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, io.MultiReader(readers...))
	if err != nil {
		return fmt.Errorf("failed to create http post request: %s, err: %v", p.endpoint, err)
	}
	req.Close = true
	req.Header.Add("Content-Type", "application/json")
	return p.sendWithCaringResponseCode(req)
}

func (p *Pusher) makeKinesisRequest(ctx context.Context, payloads []*bytes.Buffer) error {
	len := 0
	for _, p := range payloads {
		len += p.Len()
	}
	buf := bytes.NewBuffer(make([]byte, 0, len))
	for _, p := range payloads {
		if _, err := p.WriteTo(buf); err != nil {
			return err
		}
	}
	record := &firehose.Record{Data: buf.Bytes()}
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
