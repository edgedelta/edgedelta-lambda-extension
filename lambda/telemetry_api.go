package lambda

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Subscribe calls the Telemetry API to subscribe for the log events.
func (c *Client) Subscribe(ctx context.Context, logTypes []string, bufferingCfg BufferingCfg, extensionId string) error {
	data, err := json.Marshal(
		&SubscribeRequest{
			SchemaVersion: SchemaVersionLatest,
			LogTypes:      logTypes,
			BufferingCfg:  bufferingCfg,
			Destination:   destination,
		})

	if err != nil {
		return fmt.Errorf("failed to marshal SubscribeRequest, err: %v", err)
	}

	url := fmt.Sprintf("%s/%s", c.baseUrl, LambdaTelemetryEndpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewBuffer(data))
	if err != nil {
		return err
	}

	contentType := "application/json"
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(extensionIdentiferHeader, extensionId)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to subscribe to telemetry api: %v", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("%s failed: %d[%s]", url, resp.StatusCode, resp.Status)
		}
		return fmt.Errorf("%s failed: %d[%s] %s", url, resp.StatusCode, resp.Status, string(body))
	}
	return nil
}
