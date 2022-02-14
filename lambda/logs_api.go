package lambda

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
)

// Subscribe calls the Logs API to subscribe for the log events.
func (c *Client) Subscribe(logTypes []string, bufferingCfg BufferingCfg, destination Destination, extensionId string) (*SubscribeResponse, error) {
	data, err := json.Marshal(
		&SubscribeRequest{
			SchemaVersion: SchemaVersionLatest,
			LogTypes:      logTypes,
			BufferingCfg:  bufferingCfg,
			Destination:   destination,
		})

	if err != nil {
		return nil, fmt.Errorf("failed to marshal SubscribeRequest, err: %v", err)
	}

	url := fmt.Sprintf("%s/%s", c.baseUrl, LambdaLogsEndpoint)
	req, err := http.NewRequest("PUT", url, bytes.NewBuffer(data))
	if err != nil {
		return nil, err
	}

	contentType := "application/json"
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(extensionIdentiferHeader, extensionId)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("Error subscribing to logs api: %v", err)
		return nil, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Printf("%s failed: %d[%s]", url, resp.StatusCode, resp.Status)
			return nil, err
		}
		log.Printf("%s failed: %d[%s] %s", url, resp.StatusCode, resp.Status, string(body))
		return nil, err
	}

	body, _ := ioutil.ReadAll(resp.Body)
	return &SubscribeResponse{string(body)}, nil
}
