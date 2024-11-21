#!/usr/bin/env bash
set -e
set -o nounset

environment=$1
arch_type=$2
version=$3

bucket_name=$ED_DEV_SERVERLESS_REPOSITORY_BUCKET
# if environment is prod then use prod bucket
if [ "$environment" == "prod" ]; then
    bucket_name=$ED_SERVERLESS_REPOSITORY_BUCKET
fi

if [ -z "$arch_type" ]; then
    echo "Arch type is empty"
    exit 1
fi

if [ -z "$version" ]; then
    echo "Version is empty"
    exit 1
fi

project_root=$(git rev-parse --show-toplevel)
name="edgedelta-extension-layer-${arch_type}"
ext_path="bin/extensions/$name"
zip_name="layer_${arch_type}_${version}.zip"

rm -rf "${project_root}/bin"
mkdir -p "${project_root}/bin/extensions"

cd "${project_root}"
CGO_ENABLED=0 GOOS=linux GOARCH=$arch_type go build -o "$ext_path" main.go
chmod +x "$ext_path"

cd "${project_root}/bin"
zip -r "$zip_name" "extensions/"

echo "Uploading $zip_name to s3://$bucket_name/$zip_name"
aws s3 cp "$zip_name" "s3://$bucket_name/$zip_name"
