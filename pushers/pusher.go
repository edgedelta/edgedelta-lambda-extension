package pushers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/edgedelta/edgedelta-lambda-extension/cfg"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/utils"
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

type LambdaLog []map[string]interface{}

type Common struct {
	Timestamp string `json:"timestamp"`
	RequestId string `json:"request_id"`
	LogType   string `json:"log_type"`
}

type EDLog struct {
	Common
	Message  string `json:"message"`
	LogLevel string `json:"log_level"`
}

type EDMetric struct {
	Common
	DurationMs       float64 `json:"duration_ms"`
	BilledDurationMs float64 `json:"billed_duration_ms"`
	MaxMemoryUsed    float64 `json:"max_memory_used"`
	MemorySize       float64 `json:"memory_size"`
}

type Pusher struct {
	name          string
	logClient     *http.Client
	conf          *cfg.Config
	parallelism   int
	retryInterval time.Duration
	retryTimeout  time.Duration
	queue         chan []byte
	runtimeDone   chan struct{}
	stop          chan struct{}
	stopped       chan struct{}
}

// NewPusher initialize hostedenv pusher.
func NewPusher(conf *cfg.Config, logQueue chan []byte, runtimeDone chan struct{}) *Pusher {
	return &Pusher{
		conf:          conf,
		name:          "HostedEnv-Pusher",
		logClient:     newHTTPClientFunc(),
		parallelism:   conf.Parallelism,
		retryInterval: conf.RetryIntervals,
		retryTimeout:  conf.RetryTimeout,
		queue:         logQueue,
		runtimeDone:   runtimeDone,
		stop:          make(chan struct{}),
		stopped:       make(chan struct{}),
	}
}

// Start activates goroutines to consume logs.
// If a shutdown or context cancel received, flush the queue and stop all operations.
func (p *Pusher) Start(ctx context.Context) {
	for i := 0; i < p.parallelism; i++ {
		i := i
		utils.Go(fmt.Sprintf("%s.run#%d", p.name, i), func() { p.run(i, ctx) })
	}
}

func (p *Pusher) run(id int, ctx context.Context) {
	log.Printf("%s goroutine %d started running", p.name, id)
	// we need to wait until either lambda runtime is done or shutdown event received and flushing the queue.
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for {
		select {
		case item := <-p.queue:
			err := p.push(cctx, item)
			if err != nil {
				log.Printf("Error streaming data from %s, err: %v", p.name, err)
			}
		case <-cctx.Done():
			log.Printf("%s goroutine %d stopped running", p.name, id)
			return
		}
	}
}

func (p *Pusher) push(ctx context.Context, payload []byte) error {
	if payload == nil {
		return fmt.Errorf("%s post is called with nil data", p.name)
	}
	var err error
	if p.retryInterval > 0 {
		err = utils.DoWithExpBackoffC(ctx, func() error {
			return p.preprocess(ctx, payload)
		}, p.retryInterval, p.retryTimeout)
	} else {
		err = p.preprocess(ctx, payload)
	}
	return err
}

func (p *Pusher) preprocess(ctx context.Context, payload []byte) error {
	var lambdaLog LambdaLog
	err := json.Unmarshal(payload, &lambdaLog)
	if err != nil {
		log.Printf("error unmarshalling log message %s, %v", string(payload), err)
	}
	var processedLogs [][]byte
	for _, item := range lambdaLog {
		logType, ok := item["type"].(string)
		if ok && logType == "function" {
			if content, ok := item["record"].(string); ok {
				content = strings.TrimSpace(content)
				tags := strings.Split(content, "\t")
				if len(tags) < 4 {
					log.Printf("error parsing function log message %s, %v", content, err)
					continue
				}
				edLog := &EDLog{
					Common: Common{
						LogType:   logType,
						Timestamp: tags[0],
						RequestId: tags[1],
					},
				}
				edLog.LogLevel = tags[2]
				edLog.Message = tags[3]
				mLog, err := json.Marshal(edLog)
				if err != nil {
					log.Printf("error marshalling function log message %v, %v", edLog, err)
					continue
				}
				processedLogs = append(processedLogs, mLog)
			}
		} else if ok && logType == "platform.report" {
			// metrics format is:
			// {"durationMs":1251.76,"billedDurationMs":1252,"memorySizeMB":128,"maxMemoryUsedMB":70,"initDurationMs":270.81}
			if content, ok := item["record"].(map[string]interface{}); ok {
				if metric, ok := content["metrics"].(map[string]interface{}); ok {
					timestamp, ok := item["time"].(string)
					if !ok {
						timestamp = time.Now().UTC().Format(time.RFC3339)
					}
					edMetric := &EDMetric{
						Common: Common{
							LogType:   logType,
							Timestamp: timestamp,
							RequestId: content["requestId"].(string),
						},
						DurationMs:       metric["durationMs"].(float64),
						BilledDurationMs: metric["billedDurationMs"].(float64),
						MaxMemoryUsed:    metric["maxMemoryUsedMB"].(float64),
						MemorySize:       metric["memorySizeMB"].(float64),
					}
					mLog, err := json.Marshal(edMetric)
					if err != nil {
						log.Printf("error marshalling platform report message %v, %v", edMetric, err)
						continue
					}
					processedLogs = append(processedLogs, mLog)
				}
			}
		}
	}

	if len(processedLogs) > 0 {
		var multiErr []string
		for _, mlog := range processedLogs {
			if err := p.makeRequest(ctx, mlog); err != nil {
				multiErr = append(multiErr, fmt.Sprintf("cannot stream to ed endpoint: %v", err))
			}
		}
		if len(multiErr) > 0 {
			return errors.New(strings.Join(multiErr, ", "))
		}
	}

	return nil
}

func (p *Pusher) makeRequest(ctx context.Context, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.conf.EDEndpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create http post request: %s, err: %v", p.conf.EDEndpoint, err)
	}
	req.Close = true
	req.Header.Add("Content-Type", "application/json")
	return p.sendWithCaringResponseCode(req)
}

func (p *Pusher) sendWithCaringResponseCode(req *http.Request) error {
	resp, err := p.logClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		bodyBytes, err := ioutil.ReadAll(resp.Body)
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
