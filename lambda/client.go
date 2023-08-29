package lambda

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/lambda"
)

// DefaultClient for a AWS marketplace product.
type DefaultClient struct {
	svc *lambda.Lambda
}

// NewClient creates client.
func NewAWSClient(region string) (*DefaultClient, error) {
	sess, err := session.NewSession(&aws.Config{Region: aws.String(region)})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize AWS Lambda client, err: %v", err)
	}
	return &DefaultClient{svc: lambda.New(sess, &aws.Config{Region: aws.String(region)})}, nil
}

// Run given function with name with the provided request
func (c *DefaultClient) GetTags(functionARN string) (*lambda.ListTagsOutput, error) {
	result, err := c.svc.ListTags(&lambda.ListTagsInput{
		Resource: aws.String(functionARN),
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}
