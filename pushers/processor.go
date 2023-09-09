package pushers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/edgedelta/edgedelta-lambda-extension/cfg"
	"github.com/edgedelta/edgedelta-lambda-extension/lambda"
	"github.com/edgedelta/edgedelta-lambda-extension/utils"
)

const sep = '\n'

type faas struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

var faasObj = &faas{
	Name:    os.Getenv("AWS_LAMBDA_FUNCTION_NAME"),
	Version: os.Getenv("AWS_LAMBDA_FUNCTION_VERSION"),
}

type cloud struct {
	ResourceID string `json:"resource_id"`
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
	DurationMs       float64 `json:"duration_ms"`
	BilledDurationMs float64 `json:"billed_duration_ms"`
	MaxMemoryUsed    float64 `json:"max_memory_used"`
	MemorySize       float64 `json:"memory_size"`
}

type Processor struct {
	tags         map[string]string
	cloud        *cloud
	outC         chan []byte
	inC          chan []*lambda.LambdaEvent
	invokeC      chan string
	runtimeDoneC chan struct{}
	stopC        chan struct{}
	stoppedC     chan struct{}
}

// NewProcessor initializes the log processor.
func NewProcessor(conf *cfg.Config, outC chan []byte, inC chan []*lambda.LambdaEvent, runtimeDoneC chan struct{}) *Processor {
	return &Processor{
		outC:         outC,
		inC:          inC,
		invokeC:      make(chan string),
		runtimeDoneC: runtimeDoneC,
		stopC:        make(chan struct{}),
		stoppedC:     make(chan struct{}),
		tags:         conf.Tags,
		cloud:        &cloud{ResourceID: conf.FunctionARN},
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
	for {
		select {
		case events := <-p.inC:
			buf := new(bytes.Buffer)
			for _, e := range events {
				if e.EventType != lambda.Function {
					runtimeDone = handlePlatformEvent(e, requestID)
				}
				b, err := process(e, p.cloud, p.tags)
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
				runtimeDone = false
			}
		case r := <-p.invokeC:
			requestID = r
		case <-p.stopC:
			close(p.inC)
			// drain
			buf := new(bytes.Buffer)
			for events := range p.inC {
				for _, e := range events {
					b, err := process(e, p.cloud, p.tags)
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

func process(e *lambda.LambdaEvent, cloudObj *cloud, tags map[string]string) ([]byte, error) {
	eventType := e.EventType
	timestamp := e.EventTime
	record := e.Record

	switch eventType {
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
				edMetric := &edMetric{
					common: common{
						Faas:       faasObj,
						Cloud:      cloudObj,
						LogType:    eventType,
						LambdaTags: tags,
						Timestamp:  timestamp,
					},
					DurationMs:       metric["durationMs"].(float64),
					BilledDurationMs: metric["billedDurationMs"].(float64),
					MaxMemoryUsed:    metric["maxMemoryUsedMB"].(float64),
					MemorySize:       metric["memorySizeMB"].(float64),
				}
				return json.Marshal(edMetric)
			}
		}
		return nil, fmt.Errorf("failed to parse platform.report event: %v", e)
	default:
		return nil, nil
	}
}
