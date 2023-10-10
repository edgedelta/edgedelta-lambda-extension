package cfg

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/edgedelta/edgedelta-lambda-extension/lambda"
)

const (
	HTTP_PUSHER    = "http"
	KINESIS_PUSHER = "kinesis"
)

var (
	validLogTypes = map[string]bool{
		"function":  false,
		"platform":  false,
		"extension": false,
	}
)

// Config for storing all parameters
type Config struct {
	EDEndpoint         string
	KinesisEndpoint    string
	PusherMode         string
	ForwardTags        bool
	LogTypes           []string
	BfgConfig          *lambda.BufferingCfg
	PushTimeout        time.Duration
	RetryInterval      time.Duration
	Tags               map[string]string
	AccountID          string
	Region             string
	FunctionARN        string
	ProcessRuntimeName string
	HostArchitecture   string
	BufferSize         int
	FlushAtNextInvoke  bool
}

func GetConfigAndValidate() (*Config, error) {
	config := &Config{
		EDEndpoint:      os.Getenv("ED_ENDPOINT"),
		PusherMode:      os.Getenv("PUSHER_MODE"),
		KinesisEndpoint: os.Getenv("KINESIS_ENDPOINT"),
	}

	var multiErr []string
	if config.PusherMode == "" {
		config.PusherMode = HTTP_PUSHER
	}

	if config.EDEndpoint == "" && config.PusherMode == HTTP_PUSHER {
		return nil, errors.New("ED_ENDPOINT must be set as environment variable when PUSHER_MODE is set to http")
	}

	if config.KinesisEndpoint == "" && config.PusherMode == KINESIS_PUSHER {
		return nil, errors.New("KINESIS_ENDPOINT must be set as environment variable when PUSHER_MODE is set to kinesis")
	}

	config.Region = os.Getenv("AWS_REGION")

	config.ForwardTags = os.Getenv("ED_FORWARD_LAMBDA_TAGS") == "true"
	config.FlushAtNextInvoke = os.Getenv("ED_FLUSH_AT_NEXT_INVOKE") == "true"

	bufferSize := os.Getenv("ED_BUFFER_SIZE_MB")
	if bufferSize != "" {
		if i, err := strconv.ParseInt(bufferSize, 10, 0); err == nil {
			config.BufferSize = int(i) * 1000 * 1000
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse BUFFER_SIZE_MB: %v", err))
		}
	} else {
		config.BufferSize = 20 * 1000 * 1000
	}

	pushTimeout := os.Getenv("ED_PUSH_TIMEOUT_SEC")
	if pushTimeout != "" {
		if i, err := strconv.ParseInt(pushTimeout, 10, 0); err == nil {
			config.PushTimeout = time.Duration(i) * time.Second
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse PUSH_TIMEOUT_MS: %v", err))
		}
	} else {
		config.PushTimeout = 5 * time.Second
	}

	retryInterval := os.Getenv("ED_RETRY_INTERVAL_MS")
	if retryInterval != "" {
		if i, err := strconv.ParseInt(retryInterval, 10, 0); err == nil {
			config.RetryInterval = time.Duration(i) * time.Millisecond
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse RETRY_INTERVAL_MS: %v", err))
		}
	} else {
		config.RetryInterval = 100 * time.Millisecond
	}

	config.BfgConfig = &lambda.BufferingCfg{
		MaxItems:  1000,
		MaxBytes:  262144,
		TimeoutMS: 1000,
	}

	maxItems := os.Getenv("ED_LAMBDA_MAX_ITEMS")
	if maxItems != "" {
		if i, err := strconv.ParseInt(maxItems, 10, 0); err == nil {
			config.BfgConfig.MaxItems = uint32(i)
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse MAX_ITEMS: %v", err))
		}
	}

	maxBytes := os.Getenv("ED_LAMBDA_MAX_BYTES")
	if maxBytes != "" {
		if i, err := strconv.ParseInt(maxBytes, 10, 0); err == nil {
			config.BfgConfig.MaxBytes = uint32(i)
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse MAX_BYTES: %v", err))
		}
	}

	timeoutMs := os.Getenv("ED_LAMBDA_TIMEOUT_MS")
	if timeoutMs != "" {
		if i, err := strconv.ParseInt(timeoutMs, 10, 0); err == nil {
			config.BfgConfig.TimeoutMS = uint32(i)
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse TIMEOUT_MS: %v", err))
		}
	}

	logTypesStr := os.Getenv("ED_LAMBDA_LOG_TYPES")
	if logTypesStr != "" {
		logTypes := strings.Split(logTypesStr, ",")
		var usableLogTypes []string
		for _, lg := range logTypes {
			if _, ok := validLogTypes[lg]; !ok {
				multiErr = append(multiErr, fmt.Sprintf("Log type %s is not valid", lg))
				continue
			}
			usableLogTypes = append(usableLogTypes, lg)
		}
		config.LogTypes = usableLogTypes
	} else {
		config.LogTypes = []string{"platform", "function"}
	}

	var err error
	if len(multiErr) > 0 {
		err = errors.New(strings.Join(multiErr, ", "))
	}

	return config, err
}
