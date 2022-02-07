package core

import "strings"

type S3Endpoint struct {
	// AWS Access key ID
	AccessKeyID string

	// AWS Secret Access Key
	SecretAccessKey string

	// Credential path
	CredentialFilePath string

	// UseSharedCredentials for auth
	UseSharedCredentials bool

	// Region
	Region string

	// Endpoint doesn't need to be set for S3 backend.
	// For other cloud providers such as GCP endpoint need to be overwritten
	Endpoint string

	// RoleARN is the ARN of the role to assume
	RoleARN string

	// ExternalID is used while assuming a role and it prevents "confused deputy" attacks
	ExternalID string
}

// Region constants
const (
	AWSUSWest2 = "us-west-2"
	AWSUSEast1 = "us-east-1"
)

// CanUseSharedCredentials returns true if credential details are not provided
func (s *S3Endpoint) CanUseSharedCredentials() bool {
	if s.UseSharedCredentials {
		return true
	}
	return (strings.TrimSpace(s.AccessKeyID) == "" && strings.TrimSpace(s.SecretAccessKey) == "") && (strings.TrimSpace(s.RoleARN) == "") && (strings.TrimSpace(s.CredentialFilePath) == "")
}
