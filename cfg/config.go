package cfg

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/edgedelta/edgedelta-lambda-extension/lambda"
)

var (
	validLogTypes = []string{"function", "platform", "extension"}
)

// Config for storing all parameters
type Config struct {
	EDEndpoint     string
	EnableFailover bool
	S3BucketName   string
	LogTypes       []string
	BfgConfig      *lambda.BufferingCfg
	LogLevel       string
	BufferSize     int
	Parallelism    int
	RetryTimeout   time.Duration
	RetryIntervals time.Duration
}

func GetConfigAndValidate(envs map[string]string) (*Config, error) {

	config := &Config{
		EDEndpoint:     envs["ED_ENDPOINT"],
		S3BucketName:   envs["S3_BUCKET_NAME"],
		EnableFailover: false,
	}
	var multiErr []string
	if config.EDEndpoint == "" {
		multiErr = append(multiErr, "ED_ENDPOINT must be set as environment variable")
	}

	enableFailover := envs["ENABLE_FAILOVER"]
	var err error
	if enableFailover != "" {
		config.EnableFailover, err = strconv.ParseBool(enableFailover)
		if err != nil {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse ENABLE_FAILOVER: %v", err))
		}
	}

	if config.EnableFailover {
		if config.S3BucketName == "" {
			multiErr = append(multiErr, "S3_BUCKET_NAME must be set as environment variable when failover is enabled")
		}
	}

	parallelism := envs["PARALLELISM"]
	if parallelism != "" {
		if i, err := strconv.ParseInt(parallelism, 10, 0); err == nil {
			config.Parallelism = int(i)
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse PARALLELISM: %v", err))
		}
	} else {
		config.Parallelism = 1
	}

	bufferSize := envs["BUFFER_SIZE"]
	if bufferSize != "" {
		if i, err := strconv.ParseInt(bufferSize, 10, 0); err == nil {
			config.BufferSize = int(i)
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse BUFFER_SIZE: %v", err))
		}
	} else {
		config.BufferSize = 100
	}

	retryTimeout := envs["RETRY_TIMEOUT"]
	if retryTimeout != "" {
		if i, err := strconv.ParseInt(retryTimeout, 10, 0); err == nil {
			config.RetryTimeout = time.Duration(i)
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse RETRY_TIMEOUT: %v", err))
		}
	} else {
		config.RetryTimeout = 0
	}

	retryInterval := envs["RETRY_INTERVAL"]
	if retryInterval != "" {
		if i, err := strconv.ParseInt(retryInterval, 10, 0); err == nil {
			config.RetryIntervals = time.Duration(i)
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse RETRY_INTERVAL: %v", err))
		}
	} else {
		config.RetryIntervals = 0
	}

	logLevel := envs["LOG_LEVEL"]
	if logLevel != "" {
		config.LogLevel = logLevel
	} else {
		config.LogLevel = "info"
	}

	config.BfgConfig = &lambda.BufferingCfg{
		MaxItems:  1000,
		MaxBytes:  262144,
		TimeoutMS: 1000,
	}

	maxItems := envs["MAX_ITEMS"]
	if maxItems != "" {
		if i, err := strconv.ParseInt(maxItems, 10, 0); err == nil {
			config.BfgConfig.MaxItems = uint32(i)
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse MAX_ITEMS: %v", err))
		}
	}

	maxBytes := envs["MAX_BYTES"]
	if maxBytes != "" {
		if i, err := strconv.ParseInt(maxBytes, 10, 0); err == nil {
			config.BfgConfig.MaxBytes = uint32(i)
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse MAX_BYTES: %v", err))
		}
	}

	timeoutMs := envs["TIMEOUT_MS"]
	if timeoutMs != "" {
		if i, err := strconv.ParseInt(timeoutMs, 10, 0); err == nil {
			config.BfgConfig.TimeoutMS = uint32(i)
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse TIMEOUT_MS: %v", err))
		}
	}

	logTypesStr := envs["LOG_TYPES"]
	if logTypesStr != "" {
		logTypes := strings.Split(logTypesStr, ",")
		for _, lg := range logTypes {
			valid := false
			for _, vl := range validLogTypes {
				if vl == lg {
					valid = true
					break
				}
			}
			if !valid {
				multiErr = append(multiErr, fmt.Sprintf("Log type %s is not valid", lg))
			}
		}
		config.LogTypes = logTypes
	} else {
		config.LogTypes = []string{"platform", "function"}
	}
	if len(multiErr) > 0 {
		err = errors.New(strings.Join(multiErr, ", "))
	}

	return config, err
}
