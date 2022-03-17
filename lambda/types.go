package lambda

import (
	"fmt"
	"net/http"
)

const (
	extensionNameHeader      = "Lambda-Extension-Name"
	extensionIdentiferHeader = "Lambda-Extension-Identifier"
	extensionErrorType       = "Lambda-Extension-Function-Error-Type"
	LambdaLogsEndpoint       = "2020-08-15/logs"
	LambdaExtensionEndpoint  = "2020-01-01/extension"
)

// RegisterResponse is the body of the response for /register
type RegisterResponse struct {
	FunctionName    string `json:"functionName"`
	FunctionVersion string `json:"functionVersion"`
	Handler         string `json:"handler"`
}

// NextEventResponse is the response for /event/next
type NextEventResponse struct {
	EventType          ExtensionEventType `json:"eventType"`
	DeadlineMs         int64              `json:"deadlineMs"`
	RequestID          string             `json:"requestId"`
	InvokedFunctionArn string             `json:"invokedFunctionArn"`
	Tracing            Tracing            `json:"tracing"`
}

// Tracing is part of the response for /event/next
type Tracing struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// StatusResponse is the body of the response for /init/error and /exit/error
type StatusResponse struct {
	Status string `json:"status"`
}

// ExtensionEventType represents the type of events recieved from /event/next
type ExtensionEventType string

const (
	// Invoke is a lambda invoke
	Invoke ExtensionEventType = "INVOKE"

	// Shutdown is a shutdown event for the environment
	Shutdown ExtensionEventType = "SHUTDOWN"
)

const (
	// Platform is to receive logs emitted by the platform
	Platform string = "platform"
	// Function is to receive logs emitted by the function
	Function string = "function"
	// Extension is to receive logs emitted by the extension
	Extension string = "extension"
)

type SubEventType string

const (
	// RuntimeDone event is sent when lambda function is finished it's execution
	RuntimeDone    SubEventType = "platform.runtimeDone"
	RuntimeEnd     SubEventType = "platform.end"
	PlatformReport SubEventType = "platform.report"
)

// BufferingCfg is the configuration set for receiving logs from Logs API. Whichever of the conditions below is met first, the logs will be sent
type BufferingCfg struct {
	// MaxItems is the maximum number of events to be buffered in memory. (default: 10000, minimum: 1000, maximum: 10000)
	MaxItems uint32 `json:"maxItems"`
	// MaxBytes is the maximum size in bytes of the logs to be buffered in memory. (default: 262144, minimum: 262144, maximum: 1048576)
	MaxBytes uint32 `json:"maxBytes"`
	// TimeoutMS is the maximum time (in milliseconds) for a batch to be buffered. (default: 1000, minimum: 100, maximum: 30000)
	TimeoutMS uint32 `json:"timeoutMs"`
}

// URI is used to set the endpoint where the logs will be sent to
type URI string

// HttpProtocol is used to specify the protocol when subscribing to Logs API for HTTP
type HttpProtocol string

const (
	HttpProto HttpProtocol = "HTTP"
)

// Destination is the configuration for listeners who would like to receive logs with HTTP
type Destination struct {
	Protocol HttpProtocol `json:"protocol"`
	URI      URI          `json:"URI"`
}

type SchemaVersion string

const (
	SchemaVersion20210318 = "2021-03-18"
	SchemaVersionLatest   = SchemaVersion20210318
)

// SubscribeRequest is the request body that is sent to Logs API on subscribe
type SubscribeRequest struct {
	SchemaVersion SchemaVersion `json:"schemaVersion"`
	LogTypes      []string      `json:"types"`
	BufferingCfg  BufferingCfg  `json:"buffering"`
	Destination   Destination   `json:"destination"`
}

// SubscribeResponse is the response body that is received from Logs API on subscribe
type SubscribeResponse struct {
	body string
}

type LambdaLog map[string]interface{}

// Client is the client used to interact with the Lambda API Endpoints
type Client struct {
	httpClient *http.Client
	baseUrl    string
}

// NewClient returns a Lambda API client
func NewClient(awsLambdaRuntimeAPI string) *Client {
	baseUrl := fmt.Sprintf("http://%s", awsLambdaRuntimeAPI)
	return &Client{
		baseUrl:    baseUrl,
		httpClient: &http.Client{},
	}
}
