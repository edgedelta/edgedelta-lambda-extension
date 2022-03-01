package handlers

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"

	"github.com/edgedelta/edgedelta-lambda-extension/lambda"
)

// DefaultHttpListenerPort is used to set the URL where the logs will be sent by Logs API
const DefaultHttpListenerPort = "6060"

// Producer is used to listen to the Logs API using HTTP
type Producer struct {
	queue chan lambda.LambdaLog
}

func NewProducer(queue chan lambda.LambdaLog) *Producer {
	return &Producer{
		queue: queue,
	}
}

// Start initiates the server where the logs will be sent
func (pr *Producer) Start() {
	http.HandleFunc("/", pr.handleLogs)
	address := fmt.Sprintf("0.0.0.0:%s", DefaultHttpListenerPort)
	log.Printf("Listening to logs api on %s", address)
	err := http.ListenAndServe(address, nil)
	if err != nil {
		// do not need to cancel context necessarily because there might be unsent logs still in the queue.
		// Wait for the lambda runtime to finish naturally.
		log.Printf("Http Server closed, err: %v", err)
	}
}

// handleLogs handles the requests coming from the Logs API.
// Everytime Logs API sends logs, this function will read the logs from the response body
// and put them into a synchronous queue to be read by the main goroutine.
// Logging or printing besides the error cases below is not recommended if you have subscribed to receive extension logs.
// Otherwise, logging here will cause Logs API to send new logs for the printed lines which will create an infinite loop.
func (pr *Producer) handleLogs(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error reading body: %+v", err)
		return
	}
	// Puts the log message into the queue
	var lambdaLogs lambda.LambdaLogs
	err = json.Unmarshal(body, &lambdaLogs)
	if err != nil {
		log.Printf("error unmarshalling log message %s, %v", string(body), err)
	}
	for _, item := range lambdaLogs {
		pr.queue <- item
	}
}
