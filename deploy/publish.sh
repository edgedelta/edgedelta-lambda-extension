#!/usr/bin/env bash
set -ex
set -o nounset

environment=$1
arch_type=$2
version=$3

s3_bucket="ed-dev-serverless-repository"
# if environment is prod then use prod bucket
if [ "$environment" == "prod" ]; then
    s3_bucket="ed-serverless-repository"
fi


ARCH_TYPE="ARM"
if [ "$arch_type" == "amd64" ]; then
    ARCH_TYPE="AMD64"
fi
file_name="extension_${arch_type}_${version}.zip"

cat template.yml.tmpl \
| sed "s|{ARCH_TYPE}|$ARCH_TYPE|g" \
| sed "s|{BUCKET}|$s3_bucket|g" \
| sed "s|{VERSION}|$version|g" \
| sed "s|{FILE_NAME}|$file_name|g" \
> template.yml

echo "Packaging SAM template"
sam package --output-template-file packaged.yaml --s3-bucket $s3_bucket --template-file template.yaml

echo "Publishing SAM template"
sam publish --template packaged.yaml --region $AWS_DEFAULT_REGION