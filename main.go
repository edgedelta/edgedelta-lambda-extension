package main

import (
	"context"
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

func startExtension() (*Worker, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), lambda.InitTimeout)
	defer cancel()
	extensionID, err := lambdaClient.Register(ctx, extensionName)
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
}

func NewWorker(config *cfg.Config, extensionID string) *Worker {
	// Starting all producer and pusher goroutines here to make sure they will not be restarted by a warm runtime restart.
	queue := make(chan lambda.LambdaLog, config.BfgConfig.MaxItems)
	producer := handlers.NewProducer(queue)
	pusher := pushers.NewPusher(config, queue)
	return &Worker{
		ExtensionID: extensionID,
		producer:    producer,
		pusher:      pusher,
	}

}
func (w *Worker) Start() {
	w.producer.Start()
	w.pusher.Start()
}

func (w *Worker) Stop(timeout time.Duration) {
	// give a smaller timeout, so we finish gracefully
	t := timeout - 1*time.Millisecond
	w.producer.Shutdown(t)
	w.pusher.Stop(t)
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
