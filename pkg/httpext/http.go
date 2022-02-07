package httpext

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/edgedelta/edgedelta-lambda-extension/pkg/usererror"
)

// SendWithCaringResponseCode makes a request using param client and returns error if it fails or gets invalid status code
func SendWithCaringResponseCode(cl *Client, req *http.Request, name string) error {
	resp, err := cl.Do(req)
	if err != nil {
		origMsg := err.Error()
		// Handle known common transport errors
		if strings.Contains(origMsg, "no such host") {
			return usererror.New(origMsg, "Unknown endpoint host")
		}
		// See: crypto/x509/verify.go
		if strings.Contains(origMsg, "x509: certificate signed by unknown authority") {
			return usererror.New(origMsg, "TLS Verify needs to be false")
		}
		if strings.Contains(origMsg, "x509: certificate") {
			return usererror.New(origMsg, "TLS Certificate Error")
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		bodyBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("%s response body read failed err: %v", name, err)
		}
		body := string(bodyBytes)

		if body != "" {
			s := fmt.Sprintf("%s returned unexpected status code: %v response: %s", name, resp.StatusCode, body)
			return usererror.New(s, body)
		}
		return fmt.Errorf("%s returned unexpected status code: %v", name, resp.StatusCode)
	}

	return nil
}
