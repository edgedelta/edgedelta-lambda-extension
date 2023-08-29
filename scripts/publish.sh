#!/usr/bin/env bash
set -ex
set -o nounset

region="us-west-2"

name="ed-extension-layer"
ext_path="bin/extensions/$name"
zip_name="extension.zip"

rm -rf bin
mkdir -p "bin/extensions"

GOOS=linux GOARCH=amd64 go build -o "$ext_path" main.go
chmod +x "$ext_path"

cd bin
zip -r "$zip_name" "extensions/"

aws lambda publish-layer-version --layer-name "$name" --region "$region" --zip-file  "fileb://$zip_name" | jq -r '.LayerVersionArn'
cd ..
