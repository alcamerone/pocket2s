#!/usr/bin/env bash
mkdir -p ../infrastructure/lambda/http && mkdir -p ../infrastructure/lambda/ws

echo "Building HTTP executable..."
cd ../server/exec/functions/http
GOOS=linux GOARCH=arm64 go build -o bootstrap main.go
mv bootstrap ../../../../infrastructure/lambda/http

echo "Building WS executable..."
cd ../ws
GOOS=linux GOARCH=arm64 go build -o bootstrap main.go
mv bootstrap ../../../../infrastructure/lambda/ws
