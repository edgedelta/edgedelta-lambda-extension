package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/edgedelta/edgedelta-lambda-extension/cfg"
	"github.com/edgedelta/edgedelta-lambda-extension/handlers"
	"github.com/edgedelta/edgedelta-lambda-extension/lambda"
	"github.com/edgedelta/edgedelta-lambda-extension/pushers"
)

const invocationTimeoutGracePeriod = 50 * time.Millisecond

var (
	// Lambda uses the full file name of the extension to validate that the extension has completed the bootstrap sequence.
	extensionName      = path.Base(os.Args[0])
	lambdaClient       = lambda.NewClient(os.Getenv("AWS_LAMBDA_RUNTIME_API"))
	printExtensionLogs = os.Getenv("ED_PRINT_EXTENSION_LOGS")
)

func buildFunctionARN(registerResp *lambda.RegisterResponse, region string) string {
	functionName := registerResp.FunctionName
	accountID := registerResp.AccountID
	return fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", region, accountID, functionName)
}

func startExtension() (*Worker, bool) {
	if printExtensionLogs == "true" {
		log.SetOutput(io.Discard)
	}
	log.SetPrefix("[Edge Delta] ")
	log.Println("Starting edgedelta extension")
	ctx, cancel := context.WithTimeout(context.Background(), lambda.InitTimeout)
	defer cancel()
	extensionID, registerResp, err := lambdaClient.Register(ctx, extensionName)
	if err != nil {
		log.Printf("Failed to register the extension, err: %v", err)
		lambdaClient.InitError(ctx, extensionID, lambda.RegisterError, lambda.LambdaError{
			Type:    "RegistrationError",
			Message: err.Error(),
		})
		return nil, false
	}

	config, err := cfg.GetConfigAndValidate()
	if err != nil {
		log.Printf("Failed to parse config, err: %v", err)
		lambdaClient.InitError(ctx, extensionID, lambda.ConfigError, lambda.LambdaError{
			Type:    "InvalidConfig",
			Message: err.Error(),
		})
		return nil, false
	}
	region := config.Region
	functionARN := buildFunctionARN(registerResp, region)
	config.FunctionARN = functionARN
	if config.ForwardTags {
		awsClient, err := lambda.NewAWSClient(region)
		if err != nil {
			log.Printf("Failed to create AWS Lambda Client, err: %v", err)
			lambdaClient.InitError(ctx, extensionID, lambda.ClientError, lambda.LambdaError{
				Type:    "CreateClientError",
				Message: err.Error(),
			})
			return nil, false
		}
		resp, err := awsClient.GetTags(functionARN)
		if err != nil {
			log.Printf("Failed to get Lambda Tags, err: %v", err)
			lambdaClient.InitError(ctx, extensionID, lambda.ClientError, lambda.LambdaError{
				Type:    "GetTagsError",
				Message: err.Error(),
			})
			return nil, false
		}
		tags := make(map[string]string, len(resp.Tags))
		for k, v := range resp.Tags {
			tags[k] = *v
		}
		log.Printf("Found lambda tags: %v", tags)
		config.Tags = tags
	}
	worker := NewWorker(config, extensionID)
	worker.Start()

	if err := lambdaClient.Subscribe(ctx, config.LogTypes, *config.BfgConfig, extensionID); err != nil {
		log.Printf("Failed to subscribe to Telemetry API, err: %v", err)
		worker.Stop(lambda.KillTimeout)
		lambdaClient.InitError(ctx, extensionID, lambda.SubscribeError, lambda.LambdaError{
			Type:    "SubscribeError",
			Message: err.Error(),
		})
		return nil, false
	}
	return worker, true

}

type Worker struct {
	ExtensionID string
	pusher      *pushers.Pusher
	producer    *handlers.Producer
	processor   *pushers.Processor
	stopping    atomic.Bool
}

func NewWorker(config *cfg.Config, extensionID string) *Worker {
	logC := make(chan []*lambda.LambdaEvent)
	bufferC := make(chan *bytes.Buffer)
	pusher := pushers.NewPusher(config, bufferC)
	processor := pushers.NewProcessor(config, bufferC, logC)
	producer := handlers.NewProducer(logC)

	return &Worker{
		ExtensionID: extensionID,
		producer:    producer,
		pusher:      pusher,
		processor:   processor,
	}

}
func (w *Worker) Start() {
	w.pusher.Start()
	w.processor.Start()
	w.producer.Start()
}

func (w *Worker) Invoke(e *lambda.InvokeEvent) {
	timeout := time.Duration(e.DeadlineMs-time.Now().UnixMilli())*time.Millisecond - invocationTimeoutGracePeriod
	log.Printf("Invocation starts with timeout: %v", timeout)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	doneC := make(chan struct{})
	w.pusher.Invoke(ctx, doneC)
	w.processor.Invoke(e)
	select {
	case <-ctx.Done():
		return
	case <-doneC:
		return
	}
}

func (w *Worker) Stop(timeout time.Duration) bool {
	if w.stopping.CompareAndSwap(false, true) {
		log.Printf("Stopping with timeout: %v", timeout)
		timeout = timeout - 20*time.Millisecond
		deadline := time.Now().Add(timeout)
		time.Sleep(timeout / 3)
		w.producer.Shutdown(timeout / 4)
		w.processor.Stop()
		w.pusher.Stop(time.Until(deadline))
		log.Printf("Extension stopped")
		return true
	}
	return false
}

func waitSignals(cancel context.CancelFunc, worker *Worker, stop chan struct{}) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	s := <-sigs
	log.Println("Received signal:", s)
	cancel()
	if !worker.Stop(lambda.KillTimeout) { // Already stopping
		time.Sleep(lambda.KillTimeout)
	}
	stop <- struct{}{}
}

func handleInvocations(ctx context.Context, worker *Worker, stop chan struct{}) {
	// The For loop will wait for an Invoke or Shutdown event and sleep until one of them comes with Nextevent().
	for {
		// This statement signals to lambda that the extension is ready for warm restart and
		// will work until a timeout occurs or runtime crashes. Next invoke will start from here
		eventType, eventBody, err := lambdaClient.NextEvent(ctx, worker.ExtensionID)
		if err != nil {
			log.Printf("Failed to get next event, err: %v", err)
			return
		}
		log.Printf("Received next event type: %s", eventType)
		switch eventType {
		case lambda.Invoke:
			invokeEvent, err := lambda.GetInvokeEvent(eventBody)
			if err != nil {
				log.Printf("Failed to parse Invoke event, err: %v", err)
			} else {
				log.Printf("Received Invoke event: %+v", invokeEvent)
			}
			// Blocking call
			worker.Invoke(invokeEvent)
		case lambda.Shutdown:
			timeout := lambda.ShutdownTimeout
			shutdownEvent, err := lambda.GetShutdownEvent(eventBody)
			if err != nil {
				log.Printf("Failed to parse Shutdown event, err: %v", err)
			} else {
				log.Printf("Received Shutdown event: %+v", shutdownEvent)
				timeout = time.Duration(shutdownEvent.DeadlineMs-time.Now().UnixMilli()) * time.Millisecond
			}
			worker.Stop(timeout)
			stop <- struct{}{}
			return
		default:
			log.Printf("Received unexpected event type: %s", eventType)
		}
	}
}

func main() {
	worker, ok := startExtension()
	if !ok {
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := make(chan struct{})
	go handleInvocations(ctx, worker, stop)
	go waitSignals(cancel, worker, stop)
	<-stop
}
