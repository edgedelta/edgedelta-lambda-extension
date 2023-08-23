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
	functionName    = os.Getenv("AWS_LAMBDA_FUNCTION_NAME")
	functionVersion = os.Getenv("AWS_LAMBDA_FUNCTION_VERSION")
)

type Common struct {
	FaasName        string `json:"faas.name"`
	FaasVersion     string `json:"faas.version"`
	CloudResourceID string `json:"cloud.resource_id"`
	Timestamp       string `json:"timestamp"`
	LogType         string `json:"log_type"`
}

type EDLog struct {
	Common
	Message string `json:"message"`
}

type EDMetric struct {
	Common
	DurationMs       float64 `json:"duration_ms"`
	BilledDurationMs float64 `json:"billed_duration_ms"`
	MaxMemoryUsed    float64 `json:"max_memory_used"`
	MemorySize       float64 `json:"memory_size"`
}

type Pusher struct {
	name            string
	logClient       *http.Client
	kinesisClient   *firehose.Firehose
	makeRequestFunc func(context.Context, *bytes.Buffer) error
	conf            *cfg.Config
	queue           chan lambda.LambdaLog
	stop            chan time.Duration
	stopped         chan struct{}
	invokeChannels  []chan string
}

// NewPusher initialize hostedenv pusher.
func NewPusher(conf *cfg.Config, logQueue chan lambda.LambdaLog) *Pusher {
	numPushers := conf.Parallelism
	p := &Pusher{
		conf:           conf,
		queue:          logQueue,
		stop:           make(chan time.Duration, numPushers),
		stopped:        make(chan struct{}, numPushers),
		invokeChannels: make([]chan string, 0, numPushers),
	}
	// default mode is http
	switch conf.PusherMode {
	case cfg.KINESIS_PUSHER:
		p.name = "Kinesis-Pusher"
		sess := session.Must(session.NewSession())
		p.kinesisClient = firehose.New(sess)
		p.makeRequestFunc = p.makeKinesisRequest
	case cfg.HTTP_PUSHER:
		p.name = "HostedEnv-Pusher"
		p.logClient = newHTTPClientFunc()
		p.makeRequestFunc = p.makeHTTPRequest
	}
	return p
}

// ConsumeParallel activates goroutines to consume logs.
// If a shutdown or context cancel received, flush the queue and stop all operations.
func (p *Pusher) Start() {
	numPushers := p.conf.Parallelism
	for i := 0; i < numPushers; i++ {
		i := i
		invoke := make(chan string, 1)
		p.invokeChannels = append(p.invokeChannels, invoke)
		utils.Go(fmt.Sprintf("%s.run#%d", p.name, i), func() {
			p.run(i, invoke, p.conf.BufferSize, p.conf.RetryInterval, p.conf.PushTimeout, p.conf.MaxLatency)
		})
	}
}

func (p *Pusher) Invoke(functionARN string) {
	for _, c := range p.invokeChannels {
		c <- functionARN
	}
}

func (p *Pusher) Stop(timeout time.Duration) {
	numPushers := p.conf.Parallelism
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	close(p.stop)
	defer func() {
		close(p.stopped)
		for _, c := range p.invokeChannels {
			close(c)
		}
	}()
	numStopped := 0
	for {
		select {
		case <-ctx.Done():
			log.Printf("%d pushers failed to exit gracefully", numPushers-numStopped)
			return
		case <-p.stopped:
			numStopped++
			if numStopped == numPushers {
				log.Printf("All pushers exited gracefully")
				return
			}
		}
	}
}

func (p *Pusher) run(id int, invoke chan string, bufferSize int, initialRetryInterval, pushTimeout, maxLatency time.Duration) {
	// we need to wait until either lambda runtime is done or shutdown event received and flushing the queue.
	var functionARN string
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
		backoff = utils.GetExpBackoff(initialRetryInterval)
		retry <- struct{}{}
	}
	receivedCount := 0
	count := 0
	ticker := time.NewTicker(maxLatency)
	defer ticker.Stop()
	var lastPushedTime time.Time
	for {
		select {
		case arn := <-invoke:
			log.Printf("%s received invoke with function ARN: %s", logPrefix, arn)
			lastPushedTime = time.Now()
			functionARN = arn
		case item := <-p.queue:
			receivedCount++
			b, err := process(item, functionARN)
			if err != nil {
				log.Printf("%s failed to process log item %+v, err: %v", logPrefix, item, err)
				continue
			}
			if b != nil {
				count++
				buf.Write(b)
				buf.WriteRune('\n')
			}
			if buf.Len() >= bufferSize {
				startPushing(fmt.Sprintf("%s has reached max buffer size, starting pushing", logPrefix))
			}
		case t := <-ticker.C:
			if buf.Len() > 0 && t.Sub(lastPushedTime) >= maxLatency {
				startPushing(fmt.Sprintf("%s has reached max latency, starting pushing", logPrefix))
			}
		case <-retry:
			if err := p.push(buf, pushTimeout); err != nil {
				log.Printf("%s failed to push logs, err: %v", logPrefix, err)
				timer = time.AfterFunc(backoff.NextBackOff(), func() {
					retry <- struct{}{}
				})
			} else {
				lastPushedTime = time.Now()
				buf.Reset()
				backoff = nil
			}
		case t := <-p.stop:
			log.Printf("%s received %d logs, logs to send after processing: %d", logPrefix, receivedCount, count)
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

func process(payload lambda.LambdaLog, functionARN string) ([]byte, error) {
	if payload == nil {
		return nil, nil
	}
	logType, ok := payload["type"].(string)
	if !ok {
		return nil, fmt.Errorf("failed to find the type of the payload")
	}
	if logType == string(lambda.RuntimeDone) {
		return nil, nil
	} else if logType == "function" {
		if content, ok := payload["record"].(string); ok {
			timestamp, ok := payload["time"].(string)
			if !ok {
				timestamp = time.Now().UTC().Format(time.RFC3339)
			}
			content = strings.TrimSpace(content)
			edLog := &EDLog{
				Common: Common{
					FaasName:        functionName,
					FaasVersion:     functionVersion,
					CloudResourceID: functionARN,
					LogType:         logType,
					Timestamp:       timestamp,
				},
				Message: content,
			}
			b, err := json.Marshal(edLog)
			return b, err
		}
		return nil, nil
	} else if logType == "platform.report" {
		// metrics format is:
		// {"durationMs":1251.76,"billedDurationMs":1252,"memorySizeMB":128,"maxMemoryUsedMB":70,"initDurationMs":270.81}
		if content, ok := payload["record"].(map[string]interface{}); ok {
			if metric, ok := content["metrics"].(map[string]interface{}); ok {
				timestamp, ok := payload["time"].(string)
				if !ok {
					timestamp = time.Now().UTC().Format(time.RFC3339)
				}
				edMetric := &EDMetric{
					Common: Common{
						FaasName:        functionName,
						FaasVersion:     functionVersion,
						CloudResourceID: functionARN,
						LogType:         logType,
						Timestamp:       timestamp,
					},
					DurationMs:       metric["durationMs"].(float64),
					BilledDurationMs: metric["billedDurationMs"].(float64),
					MaxMemoryUsed:    metric["maxMemoryUsedMB"].(float64),
					MemorySize:       metric["memorySizeMB"].(float64),
				}
				b, err := json.Marshal(edMetric)
				return b, err
			}
		}
		return nil, nil
	}
	return nil, nil
}

func (p *Pusher) makeHTTPRequest(ctx context.Context, buf *bytes.Buffer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.conf.EDEndpoint, buf)
	if err != nil {
		return fmt.Errorf("failed to create http post request: %s, err: %v", p.conf.EDEndpoint, err)
	}
	req.Close = true
	req.Header.Add("Content-Type", "application/json")
	return p.sendWithCaringResponseCode(req)
}

func (p *Pusher) makeKinesisRequest(ctx context.Context, buf *bytes.Buffer) error {
	record := &firehose.Record{Data: buf.Bytes()}
	_, err := p.kinesisClient.PutRecordWithContext(ctx, &firehose.PutRecordInput{
		DeliveryStreamName: aws.String(p.conf.KinesisEndpoint),
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
