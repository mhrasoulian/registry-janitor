.DEFAULT_GOAL := help

.PHONY: help build test lint fmt vet

help:
	@echo "Available targets: build, test, lint, fmt, vet"

build:
	go build ./...

test:
	go test ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

vet:
	go vet ./...
