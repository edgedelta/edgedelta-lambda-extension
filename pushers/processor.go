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
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	RequestID string            `json:"request_id"`
	Step      string            `json:"step,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
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
	Cloud              *cloud           `json:"cloud"`
	Faas               *faas            `json:"faas"`
	Timestamp          string           `json:"timestamp"`
	LogType            lambda.EventType `json:"log_type"`
	HostArchitecture   string           `json:"host.arch,omitempty"`
	ProcessRuntimeName string           `json:"process.runtime.name,omitempty"`
}

type edLog struct {
	common
	Message string `json:"message"`
}

type edMetric struct {
	common
	BilledDurationMs      *float64 `json:"faas.billed_duration_ms,omitempty"`
	InitDurationMs        *float64 `json:"faas.init_duration_ms,omitempty"`
	RuntimeDurationMs     *float64 `json:"faas.runtime_duration_ms,omitempty"`
	DurationMs            *float64 `json:"faas.duration_ms,omitempty"`
	PostRuntimeDurationMs *float64 `json:"faas.post_runtime_duration_ms,omitempty"`
	MaxMemoryUsed         *float64 `json:"faas.max_memory_used,omitempty"`
	MemorySize            *float64 `json:"faas.memory_size,omitempty"`
	MemoryLeft            *float64 `json:"faas.memory_left,omitempty"`
	MemoryPercent         *float64 `json:"faas.memory_percent,omitempty"`
}

type Processor struct {
	tags           map[string]string
	cloud          *cloud
	hostArch       string
	processRuntime string
	outC           chan []byte
	inC            chan []*lambda.LambdaEvent
	invokeC        chan string
	runtimeDoneC   chan struct{}
	stopC          chan struct{}
	stoppedC       chan struct{}
}

// NewProcessor initializes the log processor.
func NewProcessor(conf *cfg.Config, outC chan []byte, inC chan []*lambda.LambdaEvent, runtimeDoneC chan struct{}) *Processor {
	return &Processor{
		outC:           outC,
		inC:            inC,
		invokeC:        make(chan string),
		runtimeDoneC:   runtimeDoneC,
		stopC:          make(chan struct{}),
		stoppedC:       make(chan struct{}),
		tags:           conf.Tags,
		cloud:          &cloud{ResourceID: conf.FunctionARN, AccountID: conf.AccountID, Region: conf.Region},
		hostArch:       conf.HostArchitecture,
		processRuntime: conf.ProcessRuntimeName,
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
	faasObj.Tags = p.tags
	faasObj.RequestID = ""
	runtimeDone := false
	for {
		select {
		case events := <-p.inC:
			buf := new(bytes.Buffer)
			for _, e := range events {
				if e.EventType != lambda.Function {
					runtimeDone = handlePlatformEvent(e, faasObj.RequestID)
				}
				b, err := process(e, p.cloud, p.hostArch, p.processRuntime)
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
				faasObj.RequestID = ""
				runtimeDone = false
			}
		case r := <-p.invokeC:
			faasObj.RequestID = r
		case <-p.stopC:
			close(p.inC)
			// drain
			buf := new(bytes.Buffer)
			for events := range p.inC {
				for _, e := range events {
					b, err := process(e, p.cloud, p.hostArch, p.processRuntime)
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

func process(e *lambda.LambdaEvent, cloudObj *cloud, hostArch, processRuntime string) ([]byte, error) {
	switch e.EventType {
	case lambda.PlatformStart:
		return processStartEvent(e, cloudObj, hostArch, processRuntime)
	case lambda.Function:
		return processLambdaFunctionEvent(e, cloudObj, hostArch, processRuntime)
	case lambda.PlatformReport:
		return processPlatformReportEvent(e, cloudObj, hostArch, processRuntime)
	case lambda.PlatformRuntimeDone:
		return processRuntimeDoneEvent(e, cloudObj, hostArch, processRuntime)
	default:
		return nil, nil
	}
}

func processStartEvent(e *lambda.LambdaEvent, cloudObj *cloud, hostArch, processRuntime string) ([]byte, error) {
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

	edLog := &edLog{
		common: common{
			Faas: &faas{
				Name:      faasObj.Name,
				Version:   faasObj.Version,
				Tags:      faasObj.Tags,
				RequestID: requestID,
				Step:      StartStep,
			},
			Cloud:              cloudObj,
			LogType:            lambda.PlatformStart,
			Timestamp:          e.EventTime,
			HostArchitecture:   hostArch,
			ProcessRuntimeName: processRuntime,
		},
		Message: fmt.Sprintf("START RequestID: %s", requestID),
	}

	return json.Marshal(edLog)
}

func processLambdaFunctionEvent(e *lambda.LambdaEvent, cloudObj *cloud, hostArch, processRuntime string) ([]byte, error) {
	if content, ok := e.Record.(string); ok {
		content = strings.TrimSpace(content)
		edLog := &edLog{
			common: common{
				Faas:               faasObj,
				Cloud:              cloudObj,
				LogType:            lambda.Function,
				Timestamp:          e.EventTime,
				HostArchitecture:   hostArch,
				ProcessRuntimeName: processRuntime,
			},
			Message: content,
		}
		return json.Marshal(edLog)
	}
	return nil, fmt.Errorf("failed to parse function event: %v", e)
}

func processPlatformReportEvent(e *lambda.LambdaEvent, cloudObj *cloud, hostArch, processRuntime string) ([]byte, error) {
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
		log.Printf("failed to get duration in platform.report event: %v", e)
	}

	billedDuration, billedOk := metric["billedDurationMs"].(float64)
	if !billedOk {
		log.Printf("failed to get billed duration in platform.report event: %v", e)
	}

	var postRuntimeDuration *float64
	if _, ok := requestDurations[requestID]; ok && durationOk {
		rd := duration - float64(requestDurations[requestID].End.Sub(requestDurations[requestID].Start).Milliseconds())
		postRuntimeDuration = &rd
		delete(requestDurations, requestID)
	}

	var runtimeDuration *float64
	if durationOk && postRuntimeDuration != nil {
		rd := billedDuration - *postRuntimeDuration
		runtimeDuration = &rd
	}

	initDurationMs, ok := metric["initDurationMs"].(float64)
	if !ok {
		log.Printf("failed to get init duration in platform.report event: %v", e)
	}

	edMetric := &edMetric{
		common: common{
			Faas: &faas{
				Name:      faasObj.Name,
				Version:   faasObj.Version,
				Tags:      faasObj.Tags,
				RequestID: requestID,
			},
			Cloud:              cloudObj,
			LogType:            lambda.PlatformReport,
			Timestamp:          e.EventTime,
			HostArchitecture:   hostArch,
			ProcessRuntimeName: processRuntime,
		},
		DurationMs:            utils.GetPointerIfNotDefaultValue(duration),
		BilledDurationMs:      utils.GetPointerIfNotDefaultValue(billedDuration),
		InitDurationMs:        utils.GetPointerIfNotDefaultValue(initDurationMs),
		MaxMemoryUsed:         utils.GetPointerIfNotDefaultValue(maxMemoryUsed),
		MemorySize:            utils.GetPointerIfNotDefaultValue(memorySize),
		RuntimeDurationMs:     runtimeDuration,
		PostRuntimeDurationMs: postRuntimeDuration,
		MemoryLeft:            memoryLeft,
		MemoryPercent:         memoryPercent,
	}

	return json.Marshal(edMetric)
}

func processRuntimeDoneEvent(e *lambda.LambdaEvent, cloudObj *cloud, hostArch, processRuntime string) ([]byte, error) {
	content, ok := e.Record.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("failed to parse platform.runtimeDone event: %v", e)
	}
	requestID, ok := content["requestId"].(string)
	if !ok {
		return nil, fmt.Errorf("failed to get request id in platform.runtimeDone event: %v", e)
	}

	if _, ok := requestDurations[requestID]; ok {
		timestampEnd, err := time.Parse(time.RFC3339Nano, e.EventTime)
		if err != nil {
			return nil, fmt.Errorf("failed to parse platform.report event: %v", e)
		}
		requestDurations[requestID].End = timestampEnd
	}

	edLog := &edLog{
		common: common{
			Faas: &faas{
				Name:      faasObj.Name,
				Version:   faasObj.Version,
				Tags:      faasObj.Tags,
				RequestID: requestID,
				Step:      EndStep,
			},
			Cloud:              cloudObj,
			LogType:            lambda.PlatformRuntimeDone,
			Timestamp:          e.EventTime,
			HostArchitecture:   hostArch,
			ProcessRuntimeName: processRuntime,
		},
		Message: fmt.Sprintf("END RequestID: %s", faasObj.RequestID),
	}

	return json.Marshal(edLog)
}
