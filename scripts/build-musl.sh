#!/usr/bin/sh

CGO_ENABLED=1 \
  CC=musl-gcc \
  GOOS=linux GOARCH=amd64 \
  go build \
  -ldflags='-linkmode external -extldflags "-static"' \
  -o ../bin/lesac ../cmd/lesac
