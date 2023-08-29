package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path"
	"syscall"
	"time"

	"github.com/edgedelta/edgedelta-lambda-extension/cfg"
	"github.com/edgedelta/edgedelta-lambda-extension/handlers"
	"github.com/edgedelta/edgedelta-lambda-extension/lambda"
	"github.com/edgedelta/edgedelta-lambda-extension/pushers"
)

var (
	// Lambda uses the full file name of the extension to validate that the extension has completed the bootstrap sequence.
	extensionName = path.Base(os.Args[0])
	lambdaClient  = lambda.NewClient(os.Getenv("AWS_LAMBDA_RUNTIME_API"))
)

func buildFunctionARN(registerResp *lambda.RegisterResponse, region string) string {
	functionName := registerResp.FunctionName
	accountID := registerResp.AccountID
	return fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", region, accountID, functionName)
}

func startExtension() (*Worker, bool) {
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
	ExtensionID         string
	pusher              *pushers.Pusher
	producer            *handlers.Producer
	runtimeDoneChannels []chan struct{}
}

func NewWorker(config *cfg.Config, extensionID string) *Worker {
	// Starting all producer and pusher goroutines here to make sure they will not be restarted by a warm runtime restart.
	numPushers := config.Parallelism
	runtimeDoneChannels := make([]chan struct{}, 0, numPushers)
	for i := 0; i < numPushers; i++ {
		runtimeDoneChannels = append(runtimeDoneChannels, make(chan struct{}, 1))
	}
	queue := make(chan lambda.LambdaEvent, config.BfgConfig.MaxItems)
	producer := handlers.NewProducer(queue, runtimeDoneChannels)
	pusher := pushers.NewPusher(config, queue, runtimeDoneChannels)
	return &Worker{
		ExtensionID:         extensionID,
		producer:            producer,
		pusher:              pusher,
		runtimeDoneChannels: runtimeDoneChannels,
	}

}
func (w *Worker) Start() {
	w.pusher.Start()
	w.producer.Start()
}

func (w *Worker) Stop(timeout time.Duration) {
	// give a smaller timeout, so we finish gracefully
	t := timeout - 1*time.Millisecond
	w.producer.Shutdown(t)
	w.pusher.Stop(t)
	for _, c := range w.runtimeDoneChannels {
		close(c)
	}
}

func main() {
	log.SetPrefix("[Edge Delta] ")
	log.Println("Starting edgedelta extension")
	worker, ok := startExtension()
	if !ok {
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	// todo could not trigger these signals on basic lambda functions -> maybe try with a long polling one
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sigs
		log.Println("Received signal:", s)
		cancel()
	}()

	// The For loop will wait for an Invoke or Shutdown event and sleep until one of them comes with Nextevent().
	for {
		select {
		case <-ctx.Done():
			log.Printf("Shutting down Edge Delta extension, context done.")
			// added this sleep for debug purposes. See goroutine stopped logs before returning.
			worker.Stop(lambda.KillTimeout)
			return
		default:
			// This statement signals to lambda that the extension is ready for warm restart and
			// will work until a timeout occurs or runtime crashes. Next invoke will start from here
			eventType, eventBody, err := lambdaClient.NextEvent(ctx, worker.ExtensionID)
			if err != nil {
				log.Printf("Error during Next Event call: %v", err)
				continue
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
			case lambda.Shutdown:
				timeout := lambda.ShutdownTimeout
				shutdownEvent, err := lambda.GetShutdownEvent(eventBody)
				if err != nil {
					log.Printf("Failed to parse Shutdown event, err: %v", err)
				} else {
					log.Printf("Received Shutdown event: %+v", shutdownEvent)
					timeout = time.Duration(shutdownEvent.DeadlineMs) * time.Millisecond
				}
				worker.Stop(timeout)
				return
			default:
				log.Printf("Received unexpected event type: %s", eventType)
			}
		}
	}
}
