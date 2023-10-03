package pushers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/edgedelta/edgedelta-lambda-extension/cfg"
	"github.com/edgedelta/edgedelta-lambda-extension/lambda"
	"github.com/edgedelta/edgedelta-lambda-extension/utils"
)

const sep = '\n'

type faas struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	RequestID string `json:"request_id,omitempty"`
}

var faasObj = &faas{
	Name:    os.Getenv("AWS_LAMBDA_FUNCTION_NAME"),
	Version: os.Getenv("AWS_LAMBDA_FUNCTION_VERSION"),
}

type cloud struct {
	ResourceID string `json:"resource_id"`
	AccountID  string `json:"account_id"`
	Region     string `json:"region"`
}

type common struct {
	Cloud      *cloud            `json:"cloud"`
	Faas       *faas             `json:"faas"`
	Timestamp  string            `json:"timestamp"`
	LogType    lambda.EventType  `json:"log_type"`
	LambdaTags map[string]string `json:"lambda_tags,omitempty"`
}

type edLog struct {
	common
	Message string `json:"message"`
}

type edMetric struct {
	common
	BilledDurationMs      float64  `json:"billed_duration_ms"`
	InitDurationMs        float64  `json:"init_duration_ms"`
	RuntimeDurationMs     *float64 `json:"runtime_duration_ms,omitempty"`
	DurationMs            float64  `json:"duration_ms"`
	PostRuntimeDurationMs float64  `json:"post_runtime_duration_ms"`
	MaxMemoryUsed         float64  `json:"max_memory_used"`
	MemorySize            float64  `json:"memory_size"`
	MemoryLeft            *float64 `json:"memory_left,omitempty"`
	MemoryPercent         *float64 `json:"memory_percent,omitempty"`
}

type requestDurations struct {
	Start time.Time
	End   time.Time
}

type Processor struct {
	tags             map[string]string
	cloud            *cloud
	outC             chan []byte
	inC              chan []*lambda.LambdaEvent
	invokeC          chan string
	runtimeDoneC     chan struct{}
	stopC            chan struct{}
	stoppedC         chan struct{}
	requestDurations *requestDurations
}

// NewProcessor initializes the log processor.
func NewProcessor(conf *cfg.Config, outC chan []byte, inC chan []*lambda.LambdaEvent, runtimeDoneC chan struct{}) *Processor {
	return &Processor{
		outC:             outC,
		inC:              inC,
		invokeC:          make(chan string),
		runtimeDoneC:     runtimeDoneC,
		stopC:            make(chan struct{}),
		stoppedC:         make(chan struct{}),
		tags:             conf.Tags,
		cloud:            &cloud{ResourceID: conf.FunctionARN, AccountID: conf.AccountID, Region: conf.Region},
		requestDurations: &requestDurations{},
	}
}

func (p *Processor) Start() {
	log.Print("Starting Log Processor")
	utils.Go("LogProcessor.run", func() {
		p.run()
	})
}

func (p *Processor) Stop() {
	log.Print("Stopping processor")
	p.stopC <- struct{}{}
	<-p.stoppedC
	log.Print("Processor stopped")
}

func (p *Processor) Invoke(e *lambda.InvokeEvent) {
	log.Print("Invoking processor")
	p.invokeC <- e.RequestID
	log.Print("Invoked processor")
}

func (p *Processor) run() {
	requestID := ""
	runtimeDone := false
	faasObj.RequestID = requestID
	for {
		select {
		case events := <-p.inC:
			buf := new(bytes.Buffer)
			for _, e := range events {
				if e.EventType != lambda.Function {
					runtimeDone = handlePlatformEvent(e, requestID)
				}
				b, err := process(e, p.cloud, p.tags, p.requestDurations)
				if err != nil {
					log.Printf("Log Processor failed to process log item %+v, err: %v", e, err)
					continue
				}
				if b != nil {
					buf.Write(b)
					buf.WriteRune(sep)
				}
			}
			p.outC <- buf.Bytes()
			if runtimeDone {
				log.Print("Runtime is done")
				p.runtimeDoneC <- struct{}{}
				requestID = ""
				faasObj.RequestID = requestID
				runtimeDone = false
			}
		case r := <-p.invokeC:
			requestID = r
			faasObj.RequestID = requestID
		case <-p.stopC:
			close(p.inC)
			// drain
			buf := new(bytes.Buffer)
			for events := range p.inC {
				for _, e := range events {
					b, err := process(e, p.cloud, p.tags, p.requestDurations)
					if err != nil {
						log.Printf("Log Processor failed to process log item %+v, err: %v", e, err)
						continue
					}
					if b != nil {
						buf.Write(b)
						buf.WriteRune(sep)
					}
				}
			}
			p.outC <- buf.Bytes()
			p.stoppedC <- struct{}{}
			return
		}
	}
}

func handlePlatformEvent(e *lambda.LambdaEvent, requestID string) bool {
	if requestID == "" {
		return false
	}
	if e.EventType != lambda.PlatformRuntimeDone {
		return false
	}
	if content, ok := e.Record.(map[string]interface{}); ok {
		if reqID, ok := content["requestId"].(string); ok {
			return reqID == requestID
		}
	}
	return false
}

func process(e *lambda.LambdaEvent, cloudObj *cloud, tags map[string]string, requestDurations *requestDurations) ([]byte, error) {
	eventType := e.EventType
	timestamp := e.EventTime
	record := e.Record

	switch eventType {
	case lambda.PlatformStart:
		var err error
		requestDurations.Start, err = time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil, fmt.Errorf("failed to parse platform.start event: %v", e)
		}
		if _, ok := record.(map[string]interface{}); !ok {
			return nil, fmt.Errorf("failed to parse platform.start event: %v", e)
		}
		tags[lambda.StepTag] = lambda.StartStep
		edLog := &edLog{
			common: common{
				Faas:       faasObj,
				Cloud:      cloudObj,
				LogType:    eventType,
				LambdaTags: tags,
				Timestamp:  timestamp,
			},
			Message: fmt.Sprintf("START RequestID: %s", faasObj.RequestID),
		}
		delete(tags, lambda.StepTag)
		return json.Marshal(edLog)
	case lambda.Function:
		if content, ok := record.(string); ok {
			content = strings.TrimSpace(content)
			edLog := &edLog{
				common: common{
					Faas:       faasObj,
					Cloud:      cloudObj,
					LogType:    eventType,
					LambdaTags: tags,
					Timestamp:  timestamp,
				},
				Message: content,
			}
			return json.Marshal(edLog)
		}
		return nil, fmt.Errorf("failed to parse function event: %v", e)
	case lambda.PlatformReport:
		// metrics format is:
		// {"durationMs":1251.76,"billedDurationMs":1252,"memorySizeMB":128,"maxMemoryUsedMB":70,"initDurationMs":270.81}
		if content, ok := record.(map[string]interface{}); ok {
			if metric, ok := content["metrics"].(map[string]interface{}); ok {
				maxMemoryUsed, maxOk := metric["maxMemoryUsedMB"].(float64)
				memorySize, sizeOk := metric["memorySizeMB"].(float64)
				var memoryLeft *float64
				var memoryPercent *float64
				if maxOk && sizeOk {
					mLeft := memorySize - maxMemoryUsed
					mPercent := mLeft / memorySize * 100
					memoryLeft = &mLeft
					memoryPercent = &mPercent
				}
				duration, durationOk := metric["durationMs"].(float64)
				if !durationOk {
					return nil, fmt.Errorf("failed to parse platform.report event: %v", e)
				}
				billedDuration, billedOk := metric["billedDurationMs"].(float64)
				if !billedOk {
					return nil, fmt.Errorf("failed to parse platform.report event: %v", e)
				}
				var runtimeDuration *float64
				if durationOk && billedOk {
					rd := duration - billedDuration
					runtimeDuration = &rd
				}

				var err error
				requestDurations.End, err = time.Parse(time.RFC3339Nano, timestamp)
				if err != nil {
					return nil, fmt.Errorf("failed to parse platform.report event: %v", e)
				}

				postRuntimeDuration := duration - float64(requestDurations.End.Sub(requestDurations.Start).Milliseconds())
				edMetric := &edMetric{
					common: common{
						Faas:       faasObj,
						Cloud:      cloudObj,
						LogType:    eventType,
						LambdaTags: tags,
						Timestamp:  timestamp,
					},
					DurationMs:            duration,
					BilledDurationMs:      billedDuration,
					InitDurationMs:        metric["initDurationMs"].(float64),
					RuntimeDurationMs:     runtimeDuration,
					PostRuntimeDurationMs: postRuntimeDuration,
					MaxMemoryUsed:         maxMemoryUsed,
					MemorySize:            memorySize,
					MemoryLeft:            memoryLeft,
					MemoryPercent:         memoryPercent,
				}
				return json.Marshal(edMetric)
			}
		}
		return nil, fmt.Errorf("failed to parse platform.report event: %v", e)

	case lambda.PlatformRuntimeDone:
		if _, ok := record.(map[string]interface{}); !ok {
			return nil, fmt.Errorf("failed to parse platform.runtimeDone event: %v", e)
		}
		tags[lambda.StepTag] = lambda.EndStep
		edLog := &edLog{
			common: common{
				Faas:       faasObj,
				Cloud:      cloudObj,
				LogType:    eventType,
				LambdaTags: tags,
				Timestamp:  timestamp,
			},
			Message: fmt.Sprintf("END RequestID: %s", faasObj.RequestID),
		}
		delete(tags, lambda.StepTag)
		return json.Marshal(edLog)
	default:
		return nil, nil
	}
}
