package session

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/aws/core"
	"github.com/edgedelta/edgedelta-lambda-extension/pkg/aws/sts"

	sess "github.com/aws/aws-sdk-go/aws/session"
)

var defaultEndpoint = &core.S3Endpoint{Region: core.AWSUSWest2, UseSharedCredentials: true}

const (
	durationForAssumeRole = 15 * time.Minute
)

type Settings struct {
	Endpoint       *core.S3Endpoint
	DisableSSL     *bool
	ForcePathStyle *bool
}

func New(configuration Settings) (*sess.Session, error) {
	if configuration.Endpoint == nil {
		configuration.Endpoint = defaultEndpoint
	}

	cfg := aws.Config{
		Region: aws.String(configuration.Endpoint.Region),
	}

	if configuration.Endpoint.Endpoint != "" {
		cfg.Endpoint = aws.String(configuration.Endpoint.Endpoint)
	}

	if !configuration.Endpoint.CanUseSharedCredentials() {
		cred, err := getCredentials(configuration.Endpoint)
		if err != nil {
			return nil, err
		}
		if cred != nil {
			cfg.Credentials = cred
		}
	}

	if configuration.DisableSSL != nil {
		cfg.DisableSSL = configuration.DisableSSL
	}
	if configuration.ForcePathStyle != nil {
		cfg.S3ForcePathStyle = configuration.ForcePathStyle
	}

	return sess.NewSession(&cfg)
}

func getCredentials(e *core.S3Endpoint) (*credentials.Credentials, error) {
	var creds *credentials.Credentials

	if e.RoleARN != "" {
		opts := []sts.Option{}
		if e.AccessKeyID != "" && e.SecretAccessKey != "" {
			opts = append(opts, sts.WithCredentials(e.AccessKeyID, e.SecretAccessKey))
		}
		cl, err := sts.NewClient(e.Region, opts...)
		if err != nil {
			return nil, fmt.Errorf("unable to initialize aws sts client err: %v", err)
		}
		var externalID *string
		if e.ExternalID != "" {
			externalID = aws.String(e.ExternalID)
		}
		creds = credentials.NewCredentials(&stscreds.AssumeRoleProvider{
			Client:     cl,
			RoleARN:    e.RoleARN,
			ExternalID: externalID,
			Duration:   durationForAssumeRole,
		})
		if _, err := creds.Get(); err != nil {
			return nil, fmt.Errorf("aws assume role %q credential error: %v", e.RoleARN, err)
		}
		return creds, nil
	}

	if e.CredentialFilePath != "" {
		creds = credentials.NewSharedCredentials(e.CredentialFilePath, "default")
		if _, err := creds.Get(); err != nil {
			return nil, fmt.Errorf("aws shared credential from file %s error: %v", e.CredentialFilePath, err)
		}
		return creds, nil
	}

	if e.AccessKeyID != "" || e.SecretAccessKey != "" {
		creds = credentials.NewStaticCredentials(e.AccessKeyID, e.SecretAccessKey, "")
		if _, err := creds.Get(); err != nil {
			return nil, fmt.Errorf("aws static credential error: %v", err)
		}
		return creds, nil
	}

	creds = credentials.NewEnvCredentials()
	if _, err := creds.Get(); err != nil {
		return nil, fmt.Errorf("aws environment credential error: %v", err)
	}
	return creds, nil
}
