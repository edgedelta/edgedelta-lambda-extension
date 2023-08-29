package lambda

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/lambda"
)

type DefaultClient struct {
	svc *lambda.Lambda
}

func NewAWSClient(region string) (*DefaultClient, error) {
	sess, err := session.NewSession(&aws.Config{Region: aws.String(region)})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize AWS Lambda client, err: %v", err)
	}
	return &DefaultClient{svc: lambda.New(sess, &aws.Config{Region: aws.String(region)})}, nil
}

func (c *DefaultClient) GetTags(functionARN string) (*lambda.ListTagsOutput, error) {
	result, err := c.svc.ListTags(&lambda.ListTagsInput{
		Resource: aws.String(functionARN),
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}
