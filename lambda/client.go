package lambda

import (
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/lambda"
)

var (
	awsDefaultRegion = os.Getenv("AWS_DEFAULT_REGION")
	awsRegion        = os.Getenv("AWS_REGION")
)

// DefaultClient for a AWS marketplace product.
type DefaultClient struct {
	svc *lambda.Lambda
}

// NewClient creates client.
func NewAWSClient() (*DefaultClient, error) {
	region := awsRegion
	if region != "" {
		region = awsDefaultRegion
	}

	sess, err := session.NewSession(&aws.Config{Region: &region})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize AWS Lambda client, err: %v", err)
	}

	return &DefaultClient{svc: lambda.New(sess, &aws.Config{Region: aws.String(region)})}, nil
}

// Run given function with name with the provided request
func (c *DefaultClient) GetTags(registerResp *RegisterResponse) (*lambda.GetFunctionOutput, error) {
	result, err := c.svc.GetFunction(&lambda.GetFunctionInput{
		FunctionName: aws.String(registerResp.FunctionName),
		Qualifier:    aws.String(registerResp.FunctionVersion),
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}
