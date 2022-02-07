package uploader

import (
	"bytes"
	"compress/gzip"
	"context"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/aws/core"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/aws/session"
)

const (
	// Note: Archiving feature uploads compressed data as 16 mb unless instructed otherwise from backend.
	defaultIngestionPartByteSize = 16*1024*1024 + 1*1024*1024 // 16 + 1 (small buffer, not to have exact matching) MB
	retryCount                   = 3
)

type Client interface {
	Upload(ctx context.Context, u *Input) error
	Stop()
}

// DefaultClient for a AWS marketplace product.
type DefaultClient struct {
	uploader        *s3manager.Uploader
	sessionSettings session.Settings
}

// Option is optional initialization parameter type
type Option func(*DefaultClient)

// WithEndpoint returns an option for setting influx endpoint
func WithEndpoint(e *core.S3Endpoint) Option {
	return func(c *DefaultClient) {
		c.sessionSettings.Endpoint = e
	}
}

func WithDisableSSL(value *bool) Option {
	return func(c *DefaultClient) {
		c.sessionSettings.DisableSSL = value
	}
}

func WithS3ForcePathStyle(value *bool) Option {
	return func(c *DefaultClient) {
		c.sessionSettings.ForcePathStyle = value
	}
}

func WithSessionSettings(settings session.Settings) Option {
	return func(c *DefaultClient) {
		c.sessionSettings = settings
	}
}

func InitializeClient(isOnPrem bool, opts ...Option) (Client, error) {
	return NewClient(opts...)
}

// NewClient returns a new uploader client
func NewClient(opts ...Option) (*DefaultClient, error) {
	cl := &DefaultClient{}

	for _, opt := range opts {
		opt(cl)
	}

	sess, err := session.New(cl.sessionSettings)
	if err != nil {
		return nil, err
	}

	cl.uploader = s3manager.NewUploader(sess, func(u *s3manager.Uploader) {
		u.PartSize = defaultIngestionPartByteSize
		u.LeavePartsOnError = true
		u.RequestOptions = append(u.RequestOptions, func(r *request.Request) {
			r.Retryable = aws.Bool(true)
			r.Retryer = client.DefaultRetryer{
				NumMaxRetries: retryCount,
			}
		})
	})
	return cl, nil
}

// Input encapsulates parameters for uploader
type Input struct {
	Key        string
	Bucket     string
	Data       *bytes.Buffer
	Compressed bool
	ACL        *string
}

// Upload passed byte buffer
func (c *DefaultClient) Upload(ctx context.Context, u *Input) (e error) {
	body := u.Data
	if u.Compressed {
		var b bytes.Buffer
		gw := gzip.NewWriter(&b)
		gw.Name = "edgedelta"
		gw.ModTime = time.Now()
		defer gw.Close()
		if _, err := gw.Write(u.Data.Bytes()); err != nil {
			return err
		}
		body = &b
	}

	_, err := c.uploader.Upload(&s3manager.UploadInput{
		Bucket: &u.Bucket,
		Key:    &u.Key,
		ACL:    u.ACL,
		// S3 upload retry only triggers if ReadSeekCloser interface met
		Body: aws.ReadSeekCloser(body),
	})
	return err
}

// Stop given client
func (c *DefaultClient) Stop() {
	c.uploader = nil
}
