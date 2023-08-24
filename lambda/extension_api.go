package lambda

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Register will register the extension with the Extensions API
func (e *Client) Register(ctx context.Context, filename string) (string, error) {
	const action = "register"
	url := fmt.Sprintf("%s/%s/%s", e.baseUrl, LambdaExtensionEndpoint, action)

	reqBody, err := json.Marshal(map[string]interface{}{
		"events": []ExtensionEventType{Invoke, Shutdown},
	})
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(reqBody))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set(extensionNameHeader, filename)
	httpRes, err := e.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	if httpRes.StatusCode != http.StatusOK {
		return "", fmt.Errorf("request failed with status %s", httpRes.Status)
	}
	defer httpRes.Body.Close()
	body, err := io.ReadAll(httpRes.Body)
	if err != nil {
		return "", err
	}
	res := RegisterResponse{}
	err = json.Unmarshal(body, &res)
	if err != nil {
		return "", err
	}
	extensionId := httpRes.Header.Get(extensionIdentiferHeader)
	return extensionId, nil
}

// NextEvent blocks while long polling for the next lambda invoke or shutdown
func (e *Client) NextEvent(ctx context.Context, extensionId string) (ExtensionEventType, []byte, error) {
	const action = "event/next"
	url := fmt.Sprintf("%s/%s/%s", e.baseUrl, LambdaExtensionEndpoint, action)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", nil, err
	}
	httpReq.Header.Set(extensionIdentiferHeader, extensionId)
	httpRes, err := e.httpClient.Do(httpReq)
	if err != nil {
		return "", nil, err
	}
	if httpRes.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("request failed with status %s", httpRes.Status)
	}
	defer httpRes.Body.Close()
	body, err := io.ReadAll(httpRes.Body)
	if err != nil {
		return "", nil, err
	}
	var res NextEvent
	err = json.Unmarshal(body, &res)
	if err != nil {
		return "", nil, err
	}
	return res.EventType, body, nil
}

// InitError is used to report an Initialization Error to lambda
func (e *Client) InitError(ctx context.Context, extensionId string, errorType FunctionErrorType, lambdaError LambdaError) error {
	const action = "init/error"
	url := fmt.Sprintf("%s/%s/%s", e.baseUrl, LambdaExtensionEndpoint, action)

	reqBody, err := json.Marshal(lambdaError)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}
	httpReq.Header.Set(extensionIdentiferHeader, extensionId)
	httpReq.Header.Set(extensionErrorType, string(errorType))

	httpRes, err := e.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	if httpRes.StatusCode != http.StatusOK {
		return fmt.Errorf("request failed with status %s", httpRes.Status)
	}
	defer httpRes.Body.Close()
	return nil
}

func GetInvokeEvent(body []byte) (*InvokeEvent, error) {
	var res InvokeEvent
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	return &res, nil

}

func GetShutdownEvent(body []byte) (*ShutdownEvent, error) {
	var res ShutdownEvent
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	return &res, nil

}
