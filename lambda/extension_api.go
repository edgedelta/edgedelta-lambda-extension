package lambda

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
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
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(reqBody))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set(extensionNameHeader, filename)
	httpRes, err := e.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	if httpRes.StatusCode != 200 {
		return "", fmt.Errorf("request failed with status %s", httpRes.Status)
	}
	defer httpRes.Body.Close()
	body, err := ioutil.ReadAll(httpRes.Body)
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
func (e *Client) NextEvent(ctx context.Context, extensionId string) (*NextEventResponse, error) {
	const action = "event/next"
	url := fmt.Sprintf("%s/%s/%s", e.baseUrl, LambdaExtensionEndpoint, action)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set(extensionIdentiferHeader, extensionId)
	httpRes, err := e.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if httpRes.StatusCode != 200 {
		return nil, fmt.Errorf("request failed with status %s", httpRes.Status)
	}
	defer httpRes.Body.Close()
	body, err := ioutil.ReadAll(httpRes.Body)
	if err != nil {
		return nil, err
	}
	res := NextEventResponse{}
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}
