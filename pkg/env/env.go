package env

import (
	"os"
)

var envVars = make(map[string]string)

func init() {
	// Initializing env vars
	envVars["ED_ENDPOINT"] = os.Getenv("ED_ENDPOINT")
	envVars["S3_BUCKET_NAME"] = os.Getenv("S3_BUCKET_NAME")
	envVars["PARALLELISM"] = os.Getenv("PARALLELISM")
	envVars["LOG_TYPES"] = os.Getenv("LOG_TYPES")
	envVars["LOG_LEVEL"] = os.Getenv("LOG_LEVEL")
	envVars["BUFFER_SIZE"] = os.Getenv("BUFFER_SIZE")
	envVars["RETRY_TIMEOUT"] = os.Getenv("RETRY_TIMEOUT")
	envVars["RETRY_INTERVAL"] = os.Getenv("RETRY_INTERVAL")
	envVars["LAMBDA_BUFFER_MAX_ITEMS"] = os.Getenv("LAMBDA_BUFFER_MAX_ITEMS")
	envVars["LAMBDA_BUFFER_MAX_BYTES"] = os.Getenv("LAMBDA_BUFFER_MAX_BYTES")
	envVars["LAMBDA_BUFFER_TIMEOUT_MS"] = os.Getenv("LAMBDA_BUFFER_TIMEOUT_MS")
	envVars["ENABLE_FAILOVER"] = os.Getenv("ENABLE_FAILOVER")

}

func Get(name string) string {
	return envVars[name]
}

func GetAllEnvs() map[string]string {
	return envVars
}

func Set(name string, val string) error {
	envVars[name] = val
	return os.Setenv(name, val)
}
