package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path"
	"syscall"

	"github.com/edgedelta/edgedelta-lambda-extension/cfg"
	"github.com/edgedelta/edgedelta-lambda-extension/handlers"
	"github.com/edgedelta/edgedelta-lambda-extension/lambda"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/env"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/log"
	"github.com/edgedelta/edgedelta-lambda-extension/pushers"
	"github.com/edgedelta/edgedelta-lambda-extension/pushers/hostedenv"
	"github.com/edgedelta/edgedelta-lambda-extension/pushers/s3"
)

var (
	extensionName = path.Base(os.Args[0])
	lambdaClient  = lambda.NewClient(os.Getenv("AWS_LAMBDA_RUNTIME_API"))
)

var config *cfg.Config
var multiPusher *pushers.MultiPusher

func init() {
	envs := env.GetAllEnvs()
	var err error
	config, err = cfg.GetConfigAndValidate(envs)
	if err != nil {
		log.Error("Problem encountered while parsing config, %v", err)
	}
	log.SetLevelWithName(config.LogLevel)
}

func bootstrapExtensionsApi(ctx context.Context) {
	_, err := lambdaClient.Register(ctx, extensionName)
	if err != nil {
		panic(err)
	}
}

func bootstrapHandlers() {
	var pArray []pushers.Pusher
	if config.EnableFailover {
		s3Pusher, err := s3.NewPusher(config)
		if err == nil {
			pArray = append(pArray, s3Pusher)
		}
	}
	hostedPusher, err := hostedenv.NewPusher(config)
	if err == nil {
		pArray = append(pArray, hostedPusher)
	}
	multiPusher = pushers.NewMultiPusher(pArray)
	producer := handlers.NewProducer(multiPusher)
	go producer.Start()

	destination := lambda.Destination{
		Protocol: lambda.HttpProto,
		URI:      lambda.URI(fmt.Sprintf("http://sandbox:%s", handlers.DefaultHttpListenerPort)),
	}

	lambdaClient.Subscribe(config.LogTypes, *config.BfgConfig, destination, lambdaClient.ExtensionID)
}

func main() {
	log.Debug("starting edgedelta extension")
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sigs
		cancel()
		log.Info("Received signal:", s)
	}()

	bootstrapExtensionsApi(ctx)
	bootstrapHandlers()
	// Finish init step
	nextResponse, err := lambdaClient.NextEvent(ctx)
	if err != nil {
		lambdaClient.InitError(ctx, err.Error())
		return
	}
	log.Debug("Received EventType: %s", nextResponse.EventType)
	// The For loop will continue till we recieve a shutdown event.
	for {
		select {
		case <-ctx.Done():
			multiPusher.FlushLogs(true)
			multiPusher.Stop()
			return
		default:
			multiPusher.FlushLogs(false)
			log.Debug("flushing logs complete for invocation")
			// This statement will freeze lambda
			nextResponse, err := lambdaClient.NextEvent(ctx)
			if err != nil {
				log.Error("Error during Next Event call: %v", err)
				return
			}
			// Next invoke will start from here
			log.Info("Received Next Event as %s", nextResponse.EventType)
			if nextResponse.EventType == lambda.Shutdown {
				multiPusher.FlushLogs(true)
				multiPusher.Stop()
				log.Debug("finished edgedelta extension, shutdown received")
				return
			}
		}
	}
}
