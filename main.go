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
	runtimeDone   chan struct{}
	extensionId   string
	queue         chan []byte
)

func startExtension(ctx context.Context, config *cfg.Config) {
	var err error
	extensionId, err = lambdaClient.Register(ctx, extensionName)
	if err != nil {
		log.Printf("Problem encountered while registering to extension, %v", err)
		os.Exit(1)
	}
	queue = make(chan []byte, config.BufferSize)
	producer := handlers.NewProducer(queue)
	go producer.Start()

	runtimeDone = make(chan struct{})
	pusher = pushers.NewPusher(config, queue, runtimeDone)
	pusher.Start()

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
	// Finish Init() step of the extension and signal the runtime that we are ready to receive logs.
	_, err = lambdaClient.NextEvent(ctx, extensionId)
	if err != nil {
		log.Printf("Problem encountered while getting next event, %v", err)
		os.Exit(3)
	}
	// The For loop will wait for an Invoke or Shutdown event and sleep until one of them comes with Nextevent().
	for {
		select {
		case <-ctx.Done():
			pusher.Stop()
			return
		default:
			// This statement signals to lambda that the extension is ready for warm restart and
			// will work until a timeout occurs or runtime crashes.
			nextResponse, err := lambdaClient.NextEvent(ctx, extensionId)
			if err != nil {
				log.Printf("Error during Next Event call: %v", err)
				return
			}
			// Next invoke will start from here
			if nextResponse.EventType == lambda.Shutdown {
				log.Printf("shutdown received, stopping extension")
				// Shutdown phase must be max 2 seconds. Leaving some time for the pusher to finish.
				tCtx, _ := context.WithTimeout(ctx, 1800*time.Millisecond)
				<-tCtx.Done()
				log.Printf("shutdown context timeout")
				pusher.Stop()
				return
			}
		}
	}
}
