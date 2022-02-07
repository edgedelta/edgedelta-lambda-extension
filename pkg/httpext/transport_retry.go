package httpext

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/edgedelta/edgedelta-lambda-extension/pkg/utils"
)

type RetryConf struct {
	totalTimeout    time.Duration
	requestTimeout  *time.Duration
	initialInterval time.Duration
	maxRetry        *int
	retryIf         RetryIfFunc
}

// RetryIfFunc used for deciding to retry for given function
type RetryIfFunc func(int, error) bool

func DefaultRetryIf(statusCode int, err error) bool {
	// Don't retry on http not implemented
	if statusCode == 501 {
		return false
	}

	// Can retry on rate limiting
	// TODO - Read "Retry-After" header to obey the back off policy between server and client
	if statusCode == http.StatusTooManyRequests {
		return true
	}

	// Let's retry on internal service errors
	if statusCode >= 500 {
		return true
	}

	if err != nil {
		// Possible good retry cases for errors are
		// Retry on io.EOF, occurs when server closes connection abruptly
		// Retry for network errors such as connection timeout
		// Consider being more spefic on non retriable errors
		return true
	}

	return false
}

// We need to consume response bodies to maintain http connections, but
// limit the size we consume to respReadLimit.
var respReadLimit = int64(4096)

// Try to read the response body so we can reuse this connection.
func drainBody(body io.ReadCloser) {
	defer body.Close()
	_, _ = io.Copy(ioutil.Discard, io.LimitReader(body, respReadLimit))
}

// retryTransport retry http roundtrip failures
type retryTransport struct {
	name string
	next http.RoundTripper
	conf *RetryConf
}

// RoundTrip overrides transport
func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var returnError error
	var returnResp *http.Response
	attempt := 0
	ctx := req.Context()
	if t.conf.requestTimeout != nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	_ = utils.DoWithExpBackoffC(ctx, func() error {
		// Cancel the request from previous failed iteration, for successful runs it should be cancelled by the caller
		if cancel != nil {
			defer cancel()
		}

		if t.conf.requestTimeout != nil {
			var reqCtx context.Context
			reqCtx, cancel = context.WithTimeout(ctx, *t.conf.requestTimeout)
			req = req.WithContext(reqCtx)
		}

		if req.Body != nil {
			if req.GetBody == nil {
				originalBody, err := ioutil.ReadAll(req.Body)
				if err != nil {
					returnError = fmt.Errorf("failed to copy body %v", err)
					return nil
				}
				if err = req.Body.Close(); err != nil {
					returnError = fmt.Errorf("failed to close body %v", err)
					return nil
				}

				req.GetBody = func() (io.ReadCloser, error) {
					return ioutil.NopCloser(bytes.NewReader(originalBody)), nil
				}
				req.Body, err = req.GetBody()
				if err != nil {
					returnError = fmt.Errorf("failed to call GetBody %v", err)
					return nil
				}
			}
			if attempt > 0 {
				// rewind body from GetBody, since it has the original copy
				var err error
				req.Body, err = req.GetBody()
				if err != nil {
					returnError = fmt.Errorf("failed to call GetBody %v", err)
					return nil
				}
			}
		}

		returnResp, returnError = t.next.RoundTrip(req) //nolint - suppressing linter because response will be closed by caller.
		statusCode := 0
		if returnResp != nil {
			statusCode = returnResp.StatusCode
		}
		attempt++

		reachedMaxLimit := t.conf.maxRetry != nil && attempt >= *t.conf.maxRetry
		shouldRetry := !reachedMaxLimit && t.conf.retryIf(statusCode, returnError)

		if !shouldRetry {
			return nil
		}

		if returnResp != nil {
			// drain previous responses for connection reuse
			// https: //github.com/google/go-github/pull/317
			drainBody(returnResp.Body)
		}

		if returnError != nil {
			return returnError
		}

		return fmt.Errorf("got response code: %d, request will be retried", returnResp.StatusCode)
	}, t.conf.initialInterval, t.conf.totalTimeout)

	if returnResp != nil && returnResp.Body != nil {
		returnResp.Body = newCustomReadCloser(returnResp.Body, cancel)
	}
	return returnResp, returnError
}

type customReadCloser struct {
	inner   io.ReadCloser
	onClose func()
}

func (w customReadCloser) Read(p []byte) (n int, err error) {
	return w.inner.Read(p)
}

func (w customReadCloser) Close() error {
	err := w.inner.Close()
	if w.onClose != nil {
		w.onClose()
	}
	return err
}

func newCustomReadCloser(r io.ReadCloser, onClose func()) io.ReadCloser {
	return customReadCloser{
		inner:   r,
		onClose: onClose,
	}
}
