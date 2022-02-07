# edgedelta-lambda-extension
Edge Delta lambda extension to monitor AWS lambda functions.

To run this example, you will need to ensure that your build architecture matches that of the Lambda execution environment by compiling with GOOS=linux and GOARCH=amd64 if you are not running in a Linux environment.

Building and saving package into a bin/extensions directory:

```
$ cd edgedelta-lambda-extension
$ GOOS=linux GOARCH=amd64 go build -o bin/extensions/edgedelta-lambda-extension main.go
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

Supported ENV_VARIABLES for Lambda Fucntion are:

- ED_ENDPOINT: Hosted agents endpoint. Required.
- ENABLE_FAILOVER: If set to true, uploads configured logs to S3_BUCKET_NAME. Default is false.
- PARALLELISM: Determines the count of streamer goroutines to consume logs. Default is 1.
- LOG_TYPES: Which types of logs you want to get from Lambda Funcion. Options are function,platform,extension. Default is function,platform.
- LOG_LEVEL: If extension logs are enabled, sets the log level of self logs. Default is info.
- BUFFER_SIZE: Buffer size of the log channel before it block newly arrived logs. Default is 100.
- RETRY_TIMEOUT: is the total duration for which to keep retry. Default is 0. This is a time.Duration() value.
- RETRY_INTERVAL: RetryInterval is the initial interval to wait until next retry. It is increased exponentially until timeout limit is reached. Default is 0 which means no retries.
  
Lambda can buffer logs and deliver them to the subscriber. You can configure this behavior in the subscription request by specifying the following optional fields.
- MAX_ITEMS: The maximum number of events to buffer in memory. Default: 1000. Minimum: 1000. Maximum: 10000.
- MAX_BYTES: The maximum size (in bytes) of the logs to buffer in memory. Default: 262144. Minimum: 262144. Maximum: 1048576.
- TIMEOUT_MS: he maximum number of events to buffer in memory. Default: 1000. Minimum: 1000. Maximum: 10000.

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
