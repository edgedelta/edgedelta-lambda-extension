# edgedelta-lambda-extension
Edge Delta lambda extension to monitor AWS lambda functions.

To run this example, you will need to ensure that your build architecture matches that of the Lambda execution environment by compiling with GOOS=linux and GOARCH=amd64 if you are not running in a Linux environment.

## Scripts

We have helper scripts in the `scripts` folder to publish and remove lambda extension as a lambda layer version.
The `publish.sh` script automates the commands in the following section.

## Manuel Build

Building and saving package into a bin/extensions directory:

```
$ cd edgedelta-lambda-extension
$ CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/extensions/edgedelta-lambda-extension main.go
$ chmod +x bin/extensions/edgedelta-lambda-extension
```

The extensions .zip file should contain a root directory called extensions/, where the extension executables are located.

```
$ cd bin
$ zip -r extension.zip extensions/
```

Publish a new layer using the extension.zip and capture the produced layer arn in layer_arn.

```
aws lambda publish-layer-version --layer-name "edgedelta-lambda-extension" --region "<use your region>" --zip-file  "fileb://extension.zip" | jq -r '.LayerVersionArn'
```

Supported ENV_VARIABLES for Lambda Function are:

- PUSHER_MODE: 'http' for hosted environments, 'kinesis' for firehose stream. Defaults to 'http'.
- ED_ENDPOINT: Hosted agents endpoint. Required if PUSHER_MODE is http.
- KINESIS_ENDPOINT: Firehose stream endpoint. Required if PUSHER_MODE is kinesis.
- ED_FORWARD_LAMBDA_TAGS: If set to 'true', logs are enriched with lambda tags (in the field "faas.tags"). Lambda function role needs to have lambda:GetFunction, lambda:ListTags and lambda:GetFunctionConfiguration permissions.
- ED_LAMBDA_LOG_TYPES: Which types of logs you want to get from Lambda Function. Options are function,platform,extension. Default is function,platform.
- ED_PUSH_TIMEOUT_SEC: Push timeout is the total duration of waiting for to send one batch of logs (in seconds). Default is 5.
- ED_RETRY_INTERVAL_MS: RetryInterval is the initial interval to wait until next retry (in milliseconds). It is increased exponentially until our process is shut down. Default is 100.
- ED_BUFFER_SIZE_MB: Maximum buffer size to keep logs (in MB). Default is 20. If stored logs exceed this limit, some logs may be dropped! 
- ED_FLUSH_AT_NEXT_INVOKE: If set to 'true', logs are flushed at the start of next invocation of the function. Normally logs are flushed after function execution finishes which can add to total running time. For lambda functions that are executed periodically, flush at next invoke can be turned on to reduce cost.
- ED_PRINT_EXTENSION_LOGS: If set to 'true', extension's own logs are printed in CloudWatch.
- ED_TAG_PREFIX: The tag prefix to be added to the source tags. For example, if ED_TAG_PREFIX is "ed_prefix_", then all the tags will be prefixed with "ed_prefix_". Default is empty.
  
Lambda can buffer logs and deliver them to the subscriber. You can configure this behavior in the subscription request by specifying the following optional fields.
- ED_LAMBDA_MAX_ITEMS: The maximum number of events to buffer in memory. Default: 1000. Minimum: 1000. Maximum: 10000. This is also the size of the channel that our http server writes into and pushers consume.
- ED_LAMBDA_MAX_BYTES: The maximum size (in bytes) of the logs to buffer in memory. Default: 262144. Minimum: 262144. Maximum: 1048576.
- ED_LAMBDA_TIMEOUT_MS: he maximum time (in milliseconds) to buffer a batch. Default: 1000. Minimum: 25. Maximum: 30000.

## Flushing Logs

See the [AWS Lambda Docs](https://docs.aws.amazon.com/lambda/latest/dg/runtimes-extensions-api.html) for explanation of lambda function phases.
Normally logs are flushed at the end of the function execution.
If ED_FLUSH_AT_NEXT_INVOKE is 'true', logs are flushed at the beginning of the next invocation of the function.
Flushes are restricted to 5 seconds at most. Failed flushes are retried for at most 10 times. Retries are done at the next flush time. 


## Local Test
In your AWS Lambda container image Dockerfile, add the command below.
```
$ cd bin
$ tar -czvf extension.tar.gz extensions
ADD <full-path-to-tar.gz-file>/extension.tar.gz /opt/
```
To verify the directory structure

```
$ docker run -it --entrypoint sh <name>:<tag>
ls -R /opt/ 
```
and check that /opt/extensions/edgedelta-lambda-extension is the directory structure you see.

Native lambda function logs come in the format: 
```
"[{\"time\":\"2022-03-01T14:09:15.227Z\",\"type\":\"platform.start\",\"record\":{\"requestId\":\"d6cd4118-2b3b-4902-9b3f-a00e4ad99e5f\",\"version\":\"$LATEST\"}},{\"time\":\"2022-03-01T14:09:15.231Z\",\"type\":\"function\",\"record\":\"2022-03-01T14:09:15.231Z\\td6cd4118-2b3b-4902-9b3f-a00e4ad99e5f\\tINFO\\tError timeout 0\\n\"},{\"time\":\"2022-03-01T14:09:15.246Z\",\"type\":\"function\",\"record\":\"2022-03-01T14:09:15.245Z\\td6cd4118-2b3b-4902-9b3f-a00e4ad99e5f\\tINFO\\tError timeout 1\\n\"},{\"time\":\"2022-03-01T14:09:15.256Z\",\"type\":\"function\",\"record\":\"2022-03-01T14:09:15.256Z\\td6cd4118-2b3b-4902-9b3f-a00e4ad99e5f\\tINFO\\tError timeout 2\\n\"},{\"time\":\"2022-03-01T14:09:15.266Z\",\"type\":\"function\",\"record\":\"2022-03-01T14:09:15.266Z\\td6cd4118-2b3b-4902-9b3f-a00e4ad99e5f\\tINFO\\tError timeout 3\\n\"},{\"time\":\"2022-03-01T14:09:15.277Z\",\"type\":\"function\",\"record\":\"2022-03-01T14:09:15.277Z\\td6cd4118-2b3b-4902-9b3f-a00e4ad99e5f\\tINFO\\tError timeout 4\\n\"},{\"time\":\"2022-03-01T14:09:15.290Z\",\"type\":\"platform.runtimeDone\",\"record\":{\"requestId\":\"d6cd4118-2b3b-4902-9b3f-a00e4ad99e5f\",\"status\":\"success\"}}]\n"
```
 
 We separate the message array and preprocess it to a more readable format: 
```
{
	"timestamp":"2022-02-17T16:23:19.243Z",
	"message":"\"2022-03-01T14:09:15.231Z\\td6cd4118-2b3b-4902-9b3f-a00e4ad99e5f\\tINFO\\tError timeout 0\\n\",
	"log_type":"function",
}

```

We also catch platform metric log in the format
```
{
    "time":"2022-02-18T11:08:42.159Z",
    "type":"platform.report",
    "record": {
        "requestId":"78b8f9e2-ee68-424d-a355-84cd91904dff",
        "metrics": {
            "durationMs":1154.11,
            "billedDurationMs":1155,
            "memorySizeMB":128,
            "maxMemoryUsedMB":68,
            "initDurationMs":216.65
        }
    }
}
```

Preprocess it to: 
```
{
	"timestamp":"2022-02-17T16:29:05.367Z",
	"request_id":"e4a6ddd8-8906-4536-b158-8cf41abdaf9b",
	"log_type":"platform.report",
	"duration_ms":1106.33,
	"billed_duration_ms":1107,
	"max_memory_used":65,
	"memory_size":128
}
```