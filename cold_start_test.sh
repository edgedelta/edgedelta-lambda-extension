#!/bin/bash

# LAMBDA_FUNCTION="tuncer-test-0203"

if [ -z "$LAMBDA_FUNCTION" ]; then
    echo "Error: LAMBDA_FUNCTION environment variable is not set."
    echo "Usage: LAMBDA_FUNCTION=<lambda_function_name> ./cold_start_test.sh"
    exit 1
fi

NUM_TESTS=10

EXISTING_ENV=$(aws lambda get-function-configuration \
    --function-name $LAMBDA_FUNCTION \
    --query 'Environment.Variables' \
    --output json)

if [ -z "$EXISTING_ENV" ]; then
    EXISTING_ENV="{}"
fi

for i in $(seq 1 $NUM_TESTS)
do
    UPDATED_ENV=$(echo "$EXISTING_ENV" | jq -c --arg v "$i" '. + {FORCE_COLD_START: $v}')

    FORMATTED_ENV=$(echo "$UPDATED_ENV" | sed 's/"/\\"/g')

    # Update Lambda environment variables to trigger cold start
    aws lambda update-function-configuration \
        --function-name $LAMBDA_FUNCTION \
        --environment "{\"Variables\":$UPDATED_ENV}" > /dev/null

    if command -v gdate >/dev/null 2>&1; then
        start_time=$(gdate +%s%3N)  # darwin
    else
        start_time=$(date +%s%3N 2>/dev/null || echo $(( $(date +%s) * 1000 )))
    fi

    aws lambda invoke --function-name $LAMBDA_FUNCTION output.txt > /dev/null

    if command -v gdate >/dev/null 2>&1; then
        end_time=$(gdate +%s%3N)
    else
        end_time=$(date +%s%3N 2>/dev/null || echo $(( $(date +%s) * 1000 )))
    fi

    # Calculate lambda overall duration
    duration=$((end_time - start_time))
    echo "Cold Start Test $i: ${duration} ms"

    sleep 5
done

echo -e "Run below query in CloudWatch Logs Insights to get cold start metrics\n"
cat <<EOF
filter @type = "REPORT"
| stats avg(@initDuration) as avgInitDuration, max(@initDuration) as maxInitDuration, min(@initDuration) as minInitDuration by bin(5m)
EOF