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
	pusher        *pushers.Pusher
	extensionId   string
	queue         chan lambda.LambdaLog
)

func startExtension(ctx context.Context, config *cfg.Config) {
	var err error
	extensionId, err = lambdaClient.Register(ctx, extensionName)
	if err != nil {
		log.Printf("Problem encountered while registering to extension, %v", err)
		os.Exit(1)
	}
	queue = make(chan lambda.LambdaLog, config.BufferSize)
	producer := handlers.NewProducer(queue)
	go producer.Start()

	pusher = pushers.NewPusher(config, queue)

	// Lambda delivers logs to a local HTTP endpoint (http://sandbox.localdomain:${PORT}/${PATH}) as an array of records in JSON format. The $PATH parameter is optional. Lambda reserves port 9001. There are no other port number restrictions or recommendations.
	destination := lambda.Destination{
		Protocol: lambda.HttpProto,
		URI:      lambda.URI(fmt.Sprintf("http://sandbox:%s", handlers.DefaultHttpListenerPort)),
	}

	lambdaClient.Subscribe(config.LogTypes, *config.BfgConfig, destination, extensionId)
}

func main() {
	log.Println("starting edgedelta extension")
	config, err := cfg.GetConfigAndValidate()
	if err != nil {
		log.Printf("fatal error while parsing config: %v", err)
		os.Exit(2)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	// todo could not trigger these signals on basic lambda functions -> maybe try with a long polling one
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sigs
		cancel()
		log.Println("Received signal:", s)
	}()

	// Starting all producer and pusher goroutines here to make sure they will not be restarted by a warm runtime restart.
	startExtension(ctx, config)
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
			nextResponse, err := lambdaClient.NextEvent(ctx, extensionId)
			if err != nil {
				log.Printf("Error during Next Event call: %v", err)
				return
			}
			log.Printf("Received next event type: %s", nextResponse.EventType)

			possibleTimeout := time.UnixMilli(nextResponse.DeadlineMs)
			timeLimitContext, timeLimitCancel := context.WithDeadline(ctx, possibleTimeout.Add(-2000*time.Millisecond))
			defer timeLimitCancel()
			pusher.ConsumeParallel(timeLimitContext)
			log.Printf("Runtime is done, exiting consume step and going to next event.")
		}
	}
}
