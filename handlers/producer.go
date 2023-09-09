package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/edgedelta/edgedelta-lambda-extension/lambda"
)

// Producer is used to listen to the Logs API using HTTP
type Producer struct {
	server *http.Server
	outC   chan []*lambda.LambdaEvent
}

func NewProducer(outC chan []*lambda.LambdaEvent) *Producer {
	return &Producer{
		outC: outC,
	}
}

// Start initiates the server where the logs will be sent
func (p *Producer) Start() {
	address := fmt.Sprintf("0.0.0.0:%s", lambda.DefaultHttpListenerPort)
	log.Printf("Listening to logs api on %s", address)
	p.server = &http.Server{Addr: address}
	http.HandleFunc("/", p.handleLogs)
	go func() {
		err := p.server.ListenAndServe()
		if err != http.ErrServerClosed {
			log.Printf("Unexpected stop on Http Server, err: %v", err)
			p.Shutdown(10 * time.Millisecond)
		} else {
			log.Print("Http Server closed")
		}
	}()
}

// handleLogs handles the requests coming from the Telemetry API.
// Everytime Logs API sends logs, this function will read the logs from the response body
// and put them into a synchronous queue to be read by the processor goroutine.
// Logging or printing besides the error cases below is not recommended if you have subscribed to receive extension logs.
// Otherwise, logging here will cause Logs API to send new logs for the printed lines which will create an infinite loop.
func (p *Producer) handleLogs(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		log.Printf("Error reading body: %+v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var events []*lambda.LambdaEvent
	if err = json.Unmarshal(body, &events); err != nil {
		log.Printf("error unmarshalling log message %s, %v", string(body), err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	p.outC <- events
	w.WriteHeader(http.StatusOK)
}

// Terminates the HTTP server listening for logs
func (p *Producer) Shutdown(timeout time.Duration) {
	if p.server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := p.server.Shutdown(ctx); err != nil {
		log.Printf("Failed to shutdown http server gracefully, err: %v", err)
	} else {
		p.server = nil
	}
}
