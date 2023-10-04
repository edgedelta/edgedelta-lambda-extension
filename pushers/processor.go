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

type requestDuration struct {
	Start time.Time
	End   time.Time
}

var (
	faasObj = &faas{
		Name:    os.Getenv("AWS_LAMBDA_FUNCTION_NAME"),
		Version: os.Getenv("AWS_LAMBDA_FUNCTION_VERSION"),
	}
	requestDurations = map[string]*requestDuration{}
)

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
	PostRuntimeDurationMs *float64 `json:"post_runtime_duration_ms,omitempty"`
	MaxMemoryUsed         float64  `json:"max_memory_used"`
	MemorySize            float64  `json:"memory_size"`
	MemoryLeft            *float64 `json:"memory_left,omitempty"`
	MemoryPercent         *float64 `json:"memory_percent,omitempty"`
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
		cloud:        &cloud{ResourceID: conf.FunctionARN, AccountID: conf.AccountID, Region: conf.Region},
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
	switch e.EventType {
	case lambda.PlatformStart:
		return processStartEvent(e, cloudObj, tags)
	case lambda.Function:
		return processLambdaFunctionEvent(e, cloudObj, tags)
	case lambda.PlatformReport:
		return processPlatformReportEvent(e, cloudObj, tags)
	case lambda.PlatformRuntimeDone:
		return processRuntimeDoneEvent(e, cloudObj, tags)
	default:
		return nil, nil
	}
}

func processStartEvent(e *lambda.LambdaEvent, cloudObj *cloud, tags map[string]string) ([]byte, error) {
	content, ok := e.Record.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("failed to parse platform.start event: %v", e)
	}
	requestID, ok := content["requestId"].(string)
	if !ok {
		return nil, fmt.Errorf("failed to get request id in platform.start event: %v", e)
	}
	start, err := time.Parse(time.RFC3339Nano, e.EventTime)
	if err != nil {
		return nil, fmt.Errorf("failed to parse timestamp of platform.start event: %v", e)
	}
	requestDurations[requestID] = &requestDuration{Start: start}
	cTags := utils.CopyMap(tags)
	cTags[lambda.StepTag] = lambda.StartStep

	edLog := &edLog{
		common: common{
			Faas:       &faas{Name: faasObj.Name, Version: faasObj.Version, RequestID: requestID},
			Cloud:      cloudObj,
			LogType:    lambda.PlatformStart,
			LambdaTags: cTags,
			Timestamp:  e.EventTime,
		},
		Message: fmt.Sprintf("START RequestID: %s", requestID),
	}
	return json.Marshal(edLog)
}

func processLambdaFunctionEvent(e *lambda.LambdaEvent, cloudObj *cloud, tags map[string]string) ([]byte, error) {
	if content, ok := e.Record.(string); ok {
		content = strings.TrimSpace(content)
		edLog := &edLog{
			common: common{
				Faas:       faasObj,
				Cloud:      cloudObj,
				LogType:    lambda.Function,
				LambdaTags: tags,
				Timestamp:  e.EventTime,
			},
			Message: content,
		}
		return json.Marshal(edLog)
	}
	return nil, fmt.Errorf("failed to parse function event: %v", e)
}

func processPlatformReportEvent(e *lambda.LambdaEvent, cloudObj *cloud, tags map[string]string) ([]byte, error) {
	// metrics format is:
	// {"durationMs":1251.76,"billedDurationMs":1252,"memorySizeMB":128,"maxMemoryUsedMB":70,"initDurationMs":270.81}
	content, ok := e.Record.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("failed to parse content of platform.report event: %v", e)
	}
	metric, ok := content["metrics"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("failed to parse metrics attribute of platform.report event: %v", e)
	}
	requestID, ok := content["requestId"].(string)
	if !ok {
		return nil, fmt.Errorf("failed to get request id in platform.report event: %v", e)
	}

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

	timestampEnd, err := time.Parse(time.RFC3339Nano, e.EventTime)
	if err != nil {
		return nil, fmt.Errorf("failed to parse platform.report event: %v", e)
	}

	var postRuntimeDuration *float64
	if _, ok := requestDurations[requestID]; ok {
		requestDurations[requestID].End = timestampEnd
		rd := duration - float64(requestDurations[requestID].End.Sub(requestDurations[requestID].Start).Milliseconds())
		postRuntimeDuration = &rd
		delete(requestDurations, requestID)
	}
	edMetric := &edMetric{
		common: common{
			Faas:       &faas{Name: faasObj.Name, Version: faasObj.Version, RequestID: requestID},
			Cloud:      cloudObj,
			LogType:    lambda.PlatformReport,
			LambdaTags: tags,
			Timestamp:  e.EventTime,
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

func processRuntimeDoneEvent(e *lambda.LambdaEvent, cloudObj *cloud, tags map[string]string) ([]byte, error) {
	content, ok := e.Record.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("failed to parse platform.runtimeDone event: %v", e)
	}
	requestID, ok := content["requestId"].(string)
	if !ok {
		return nil, fmt.Errorf("failed to get request id in platform.runtimeDone event: %v", e)
	}
	cTags := utils.CopyMap(tags)
	cTags[lambda.StepTag] = lambda.EndStep
	edLog := &edLog{
		common: common{
			Faas:       &faas{Name: faasObj.Name, Version: faasObj.Version, RequestID: requestID},
			Cloud:      cloudObj,
			LogType:    lambda.PlatformRuntimeDone,
			LambdaTags: cTags,
			Timestamp:  e.EventTime,
		},
		Message: fmt.Sprintf("END RequestID: %s", faasObj.RequestID),
	}
	return json.Marshal(edLog)
}
