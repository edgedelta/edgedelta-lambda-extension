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
	EDEndpoint      string
	KinesisEndpoint string
	PusherMode      string
	LogTypes        []string
	BfgConfig       *lambda.BufferingCfg
	BufferSize      int
	Parallelism     int
	PushTimeout     time.Duration
	RetryInterval   time.Duration
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

	parallelism := os.Getenv("ED_PARALLELISM")
	if parallelism != "" {
		if i, err := strconv.ParseInt(parallelism, 10, 0); err == nil {
			config.Parallelism = int(i)
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse PARALLELISM: %v", err))
		}
	} else {
		config.Parallelism = 4
	}

	pushTimeout := os.Getenv("ED_PUSH_TIMEOUT_MS")
	if pushTimeout != "" {
		if i, err := strconv.ParseInt(pushTimeout, 10, 0); err == nil {
			config.PushTimeout = time.Duration(i) * time.Millisecond
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse PUSH_TIMEOUT: %v", err))
		}
	} else {
		config.PushTimeout = 500 * time.Millisecond
	}

	retryInterval := os.Getenv("ED_RETRY_INTERVAL_MS")
	if retryInterval != "" {
		if i, err := strconv.ParseInt(retryInterval, 10, 0); err == nil {
			config.RetryInterval = time.Duration(i) * time.Millisecond
		} else {
			multiErr = append(multiErr, fmt.Sprintf("Unable to parse RETRY_INTERVAL: %v", err))
		}
	} else {
		config.RetryInterval = 10 * time.Millisecond
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
	// https://docs.aws.amazon.com/lambda/latest/dg/telemetry-api.html
	config.BufferSize = int(2*config.BfgConfig.MaxBytes + 300*config.BfgConfig.MaxItems) / config.Parallelism

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
