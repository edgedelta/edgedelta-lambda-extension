#!/usr/bin/env bash
set -ex
set -o nounset

region="us-west-2"

name="ed-extension-layer"
version="3"

aws lambda delete-layer-version --layer-name "$name" --region "$region" --version-number "$version"
