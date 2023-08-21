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

func startExtension() (*cfg.Config, bool) {
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

	if err := lambdaClient.Subscribe(ctx, config.LogTypes, *config.BfgConfig, extensionID); err != nil {
		log.Printf("Failed to subscribe to telemetry API, err: %v", err)
		lambdaClient.InitError(ctx, extensionID, lambda.SubscribeError, lambda.LambdaError{
			Type:    "SubscriptionError",
			Message: err.Error(),
		})
		return nil, false
	}
	config.ExtensionID = extensionID
	return config, true

}

func handleShutdown(body []byte) {

}

func handleInvoke(body []byte) {
	
}

func main() {
	log.Println("starting edgedelta extension")
	config, ok := startExtension()
	if !ok {
		os.Exit(1)
	}
	// Starting all producer and pusher goroutines here to make sure they will not be restarted by a warm runtime restart.
	queue := make(chan lambda.LambdaLog, config.BufferSize)
	producer := handlers.NewProducer(queue)
	go producer.Start()

	pusher := pushers.NewPusher(config, queue)

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
			time.Sleep(100 * time.Millisecond)
			return
		default:
			// This statement signals to lambda that the extension is ready for warm restart and
			// will work until a timeout occurs or runtime crashes. Next invoke will start from here
			eventType, eventBody, err := lambdaClient.NextEvent(ctx, config.ExtensionID)
			if err != nil {
				log.Printf("Error during Next Event call: %v", err)
				return
			}
			log.Printf("Received next event type: %s", eventType)
			switch eventType {
				case lambda.Invoke:
					handleInvoke(eventBody)
				case lambda.Shutdown:
					handleShutdown(eventBody)

			}

			possibleTimeout := time.UnixMilli(nextResponse.DeadlineMs)
			timeLimitContext, timeLimitCancel := context.WithDeadline(ctx, possibleTimeout.Add(-2000*time.Millisecond))
			defer timeLimitCancel()
			pusher.ConsumeParallel(timeLimitContext)
			log.Printf("Runtime is done, exiting consume step and going to next event.")
		}
	}
}
