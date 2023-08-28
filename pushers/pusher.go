package pushers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cenkalti/backoff"
	"github.com/edgedelta/edgedelta-lambda-extension/cfg"
	"github.com/edgedelta/edgedelta-lambda-extension/lambda"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/utils"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/firehose"
)

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

type faas struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type cloud struct {
	ResourceID string `json:"resource_id"`
}

type common struct {
	Cloud     *cloud           `json:"cloud"`
	Faas      *faas            `json:"faas"`
	Timestamp string           `json:"timestamp"`
	LogType   lambda.EventType `json:"log_type"`
}

type edLog struct {
	common
	Message string `json:"message"`
}

type edMetric struct {
	common
	DurationMs       float64 `json:"duration_ms"`
	BilledDurationMs float64 `json:"billed_duration_ms"`
	MaxMemoryUsed    float64 `json:"max_memory_used"`
	MemorySize       float64 `json:"memory_size"`
}

type Pusher struct {
	name                string
	logClient           *http.Client
	kinesisClient       *firehose.Firehose
	makeRequestFunc     func(context.Context, *bytes.Buffer) error
	endpoint            string
	numPushers          int
	bufferSize          int
	retryInterval       time.Duration
	pushTimeout         time.Duration
	maxLatency          time.Duration
	queue               chan lambda.LambdaEvent
	stop                chan time.Duration
	stopped             chan struct{}
	invokeChannels      []chan string
	runtimeDoneChannels []chan struct{}
}

var faasObj = &faas{
	Name:    os.Getenv("AWS_LAMBDA_FUNCTION_NAME"),
	Version: os.Getenv("AWS_LAMBDA_FUNCTION_VERSION"),
}

// NewPusher initialize hostedenv pusher.
func NewPusher(conf *cfg.Config, logQueue chan lambda.LambdaEvent) *Pusher {
	numPushers := conf.Parallelism
	p := &Pusher{
		queue:               logQueue,
		numPushers:          numPushers,
		bufferSize:          conf.BufferSize,
		retryInterval:       conf.RetryInterval,
		pushTimeout:         conf.PushTimeout,
		maxLatency:          conf.MaxLatency,
		stop:                make(chan time.Duration, numPushers),
		stopped:             make(chan struct{}, numPushers),
		invokeChannels:      make([]chan string, 0, numPushers),
		runtimeDoneChannels: make([]chan struct{}, 0, numPushers),
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

// ConsumeParallel activates goroutines to consume logs.
// If a shutdown or context cancel received, flush the queue and stop all operations.
func (p *Pusher) Start() {
	log.Printf("Starting %d pushers with buffer size: %d bytes, retryInterval: %v, pushTimeout: %v, maxLatency: %v",
		p.numPushers, p.bufferSize, p.retryInterval, p.pushTimeout, p.maxLatency)
	for i := 0; i < p.numPushers; i++ {
		i := i
		invoke := make(chan string, 1)
		p.invokeChannels = append(p.invokeChannels, invoke)
		runtimeDone := make(chan struct{}, 1)
		p.runtimeDoneChannels = append(p.runtimeDoneChannels, runtimeDone)
		utils.Go(fmt.Sprintf("%s.run#%d", p.name, i), func() {
			p.run(i, invoke, runtimeDone)
		})
	}
}

func (p *Pusher) Invoke(functionARN string) {
	for _, c := range p.invokeChannels {
		c <- functionARN
	}
}

func (p *Pusher) RuntimeDone() {
	log.Printf("Function invocation finished")
	for _, c := range p.runtimeDoneChannels {
		c <- struct{}{}
	}
}

func (p *Pusher) Stop(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for i := 0; i < p.numPushers; i++ {
		p.stop <- timeout
	}
	defer func() {
		close(p.stopped)
		close(p.stop)
		for _, c := range p.invokeChannels {
			close(c)
		}
		for _, c := range p.runtimeDoneChannels {
			close(c)
		}
	}()
	numStopped := 0
	for {
		select {
		case <-ctx.Done():
			log.Printf("%d pushers failed to exit gracefully", p.numPushers-numStopped)
			return
		case <-p.stopped:
			numStopped++
			if numStopped == p.numPushers {
				log.Printf("All pushers exited gracefully")
				return
			}
		}
	}
}

func (p *Pusher) run(id int, invoke chan string, runtimeDone chan struct{}) {
	// we need to wait until either lambda runtime is done or shutdown event received and flushing the queue.
	var cloudObj *cloud
	logPrefix := fmt.Sprintf("%s-%d", p.name, id)
	buf := new(bytes.Buffer)
	var backoff *backoff.ExponentialBackOff
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	retry := make(chan struct{}, 1)
	startPushing := func(msg string) {
		if backoff != nil {
			return
		}
		log.Print(msg)
		backoff = utils.GetExpBackoff(p.retryInterval)
		retry <- struct{}{}
	}
	receivedCount := 0
	count := 0
	ticker := time.NewTicker(p.maxLatency)
	defer ticker.Stop()
	var lastPushedTime time.Time
	for {
		select {
		case arn := <-invoke:
			log.Printf("%s received invoke with function ARN: %s", logPrefix, arn)
			lastPushedTime = time.Now()
			cloudObj = &cloud{ResourceID: arn}
		case event := <-p.queue:
			receivedCount++
			if event.EventType == lambda.PlatformRuntimeDone {
				p.RuntimeDone()
				continue
			}
			b, err := process(event, cloudObj)
			if err != nil {
				log.Printf("%s failed to process log item %+v, err: %v", logPrefix, event, err)
				continue
			}
			if b != nil {
				count++
				buf.Write(b)
				buf.WriteRune('\n')
			}
			if buf.Len() >= p.bufferSize {
				startPushing(fmt.Sprintf("%s has reached max buffer size, pushing", logPrefix))
			}
		case <-runtimeDone:
			startPushing(fmt.Sprintf("%s received runtime done event, pushing", logPrefix))
		case t := <-ticker.C:
			if buf.Len() > 0 && t.Sub(lastPushedTime) >= p.maxLatency {
				startPushing(fmt.Sprintf("%s has reached max latency, pushing", logPrefix))
			}
		case <-retry:
			if err := p.push(buf, p.pushTimeout); err != nil {
				log.Printf("%s failed to push logs, err: %v", logPrefix, err)
				timer = time.AfterFunc(backoff.NextBackOff(), func() {
					retry <- struct{}{}
				})
			} else {
				log.Printf("%s pushed logs", logPrefix)
				lastPushedTime = time.Now()
				buf.Reset()
				backoff = nil
			}
		case t := <-p.stop:
			log.Printf("%s received %d logs, logs sent after processing: %d", logPrefix, receivedCount, count)
			if err := p.push(buf, t); err != nil {
				log.Printf("%s failed to push logs, err: %v", logPrefix, err)
			}
			p.stopped <- struct{}{}
			return
		}
	}
}

func (p *Pusher) push(buf *bytes.Buffer, timeout time.Duration) error {
	if buf.Len() == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return p.makeRequestFunc(ctx, buf)
}

func process(event lambda.LambdaEvent, cloudObj *cloud) ([]byte, error) {
	eventType := event.EventType
	timestamp := event.EventTime
	record := event.Record

	switch eventType {
	case lambda.Function:
		if content, ok := record.(string); ok {
			content = strings.TrimSpace(content)
			edLog := &edLog{
				common: common{
					Faas:      faasObj,
					Cloud:     cloudObj,
					LogType:   eventType,
					Timestamp: timestamp,
				},
				Message: content,
			}
			return json.Marshal(edLog)
		}
		return nil, fmt.Errorf("failed to parse function event: %v", event)
	case lambda.PlatformReport:
		// metrics format is:
		// {"durationMs":1251.76,"billedDurationMs":1252,"memorySizeMB":128,"maxMemoryUsedMB":70,"initDurationMs":270.81}
		if content, ok := record.(map[string]interface{}); ok {
			if metric, ok := content["metrics"].(map[string]interface{}); ok {
				edMetric := &edMetric{
					common: common{
						Faas:      faasObj,
						Cloud:     cloudObj,
						LogType:   eventType,
						Timestamp: timestamp,
					},
					DurationMs:       metric["durationMs"].(float64),
					BilledDurationMs: metric["billedDurationMs"].(float64),
					MaxMemoryUsed:    metric["maxMemoryUsedMB"].(float64),
					MemorySize:       metric["memorySizeMB"].(float64),
				}
				return json.Marshal(edMetric)
			}
		}
		return nil, fmt.Errorf("failed to parse platform.report event: %v", event)
	default:
		return nil, nil
	}
}

func (p *Pusher) makeHTTPRequest(ctx context.Context, buf *bytes.Buffer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, buf)
	if err != nil {
		return fmt.Errorf("failed to create http post request: %s, err: %v", p.endpoint, err)
	}
	req.Close = true
	req.Header.Add("Content-Type", "application/json")
	return p.sendWithCaringResponseCode(req)
}

func (p *Pusher) makeKinesisRequest(ctx context.Context, buf *bytes.Buffer) error {
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
