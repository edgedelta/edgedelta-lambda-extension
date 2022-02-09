package handlers

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
)

// DefaultHttpListenerPort is used to set the URL where the logs will be sent by Logs API
const DefaultHttpListenerPort = "6060"

// Producer is used to listen to the Logs API using HTTP
type Producer struct {
	queue chan []byte
}

func NewProducer(queue chan []byte) *Producer {
	return &Producer{
		queue: queue,
	}
}

// Start initiates the server where the logs will be sent
//todo pass ctx here for cancel
func (pr *Producer) Start() {
	http.HandleFunc("/", pr.handleLogs)
	address := fmt.Sprintf("0.0.0.0:%s", DefaultHttpListenerPort)
	log.Printf("Listening to logs api on %s", address)
	err := http.ListenAndServe(address, nil)
	if err != nil {
		//todo should not continue from here fatalf maybe
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
	pr.queue <- body
}
