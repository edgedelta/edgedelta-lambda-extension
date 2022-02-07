package sts

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sts"
)

// Client for AWS Security Token Service (STS). ref: https://docs.aws.amazon.com/sdk-for-go/api/service/sts
type Client struct {
	svc   *sts.STS
	creds *credentials.Credentials
}

type Option func(*Client) error

// WithCredentials sets the credentials for the client
func WithCredentials(accessKeyID, secretAccessKey string) Option {
	return func(c *Client) error {
		creds := credentials.NewStaticCredentials(accessKeyID, secretAccessKey, "")
		if _, err := creds.Get(); err != nil {
			return fmt.Errorf("aws static credential error: %v", err)
		}
		c.creds = creds
		return nil
	}
}

// NewClient returns a new uploader client
func NewClient(region string, opts ...Option) (*Client, error) {
	c := &Client{}
	for _, o := range opts {
		if err := o(c); err != nil {
			return nil, err
		}
	}
	sess, err := session.NewSession(&aws.Config{Region: &region, Credentials: c.creds})
	if err != nil {
		return nil, fmt.Errorf("unable to initialize aws sts client error: %v", err)
	}
	c.svc = sts.New(sess)
	return c, nil
}

// AssumeRole API operation for AWS Security Token Service.
// ref: https://docs.aws.amazon.com/sdk-for-go/api/service/sts/#AssumeRole
func (c *Client) AssumeRole(in *sts.AssumeRoleInput) (out *sts.AssumeRoleOutput, e error) {
	return c.svc.AssumeRole(in)
}

// Stop given client
func (c *Client) Stop() error {
	c.svc = nil
	return nil
}
