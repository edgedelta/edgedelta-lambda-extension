#!/usr/bin/env bash
set -ex
set -o nounset

region="us-west-2"
project_root=$(git rev-parse --show-toplevel)

dir="bin/extensions"
name="ed-extension-layer"
ext_path="$dir/$name"
zip_path="bin/extension.zip"

mkdir -p "$dir"

GOOS=linux GOARCH=amd64 go build -o "$ext_path" main.go
chmod +x "$ext_path"


zip -r "$zip_path" "$dir"

aws lambda publish-layer-version --layer-name "$name" --region "$region" --zip-file  "fileb://$zip_path" | jq -r '.LayerVersionArn'
