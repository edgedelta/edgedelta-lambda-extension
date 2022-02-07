package httpext

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/edgedelta/edgedelta-lambda-extension/pkg/log"
)

var (
	clientsMu     sync.Mutex
	clients       = make(map[string]bool)
	maxRetryCount = 3
)

// Option configures a Server.
type Option func(c *Client)

// Client is an observable http client.
type Client struct {
	*http.Client
	name           string
	retryTransport *retryTransport
}

// generateName returns a unique name for http client.
// Even multiple callers use the same prefix array generateName ensures they have unique names by appending "/n" suffix.
// e.g. prefix = {"sumo", "client"}
// 		first caller will have name = "sumo/client"
// 		second caller will have name = "sumo/client/2"
// 		third caller will have name = "sumo/client/3" and so on
func generateName(prefix []string) string {
	base := strings.Join(prefix, "/")
	clientsMu.Lock()
	defer clientsMu.Unlock()

	name := base
	for i := 1; ; i++ {
		if clients[name] {
			name = fmt.Sprintf("%s/%d", base, i)
			continue
		}
		clients[name] = true
		return name
	}
}

// NewClient creates an Observable http Client
// s stands for an array of prefix of client that helps to differentiate in logs and look at the metric results
// traceOn used for http observing never forget that working with trace cause delay on http execution
// First option in the list will be applied first so order is important
func NewClient(n []string, client *http.Client, opts ...Option) *Client {
	c := &Client{
		client,
		generateName(n),
		nil,
	}

	// apply the list of options to client
	for _, opt := range opts {
		opt(c)
	}

	return c
}

// WithRetry enables exponential retry for http client.
// It retries for all returned erroring (err != nil) client.Do calls
// It retries for given http response status code ranges for client.Do calls.
// It will only retry till the given total timeout including sum of all retry spans.
// if requestTimeout is not nil, then context passed through request is overwritten with given timeout
// Be careful when using WithRetry, make sure all endpoints on the client is idempotent.
// Pick MaxInterval carefully, logically it should >= http.client.Timeout
// Important order of WithRetry, WithTracing, WithStat will change the outcome, pick order as needed.
func WithRetry(initialInterval time.Duration, requestTimeout time.Duration, totalTimeout time.Duration, retryIf RetryIfFunc) Option {
	return func(c *Client) {
		c.retryTransport = &retryTransport{
			name: c.name,
			conf: &RetryConf{
				totalTimeout,
				&requestTimeout,
				initialInterval,
				&maxRetryCount,
				retryIf,
			},
			next: c.Transport,
		}
		c.Transport = c.retryTransport
	}
}

// Name returns generated name for the client from given prefixes e.g. api.edgedelta.com/v1
func (c *Client) Name() string { return c.name }

// Stop stops idle connections hoping to prevent go routine leaks.
func (c *Client) Stop() {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if !clients[c.name] {
		return
	}

	if c.Client != nil {
		c.Client.CloseIdleConnections()
	}

	delete(clients, c.name)
	log.Debug("http client %s stopped", c.name)
}
