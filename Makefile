.DEFAULT_GOAL := help

.PHONY: help build test lint fmt vet up seed demo down clean

help:
	@echo "Available targets: build, test, lint, fmt, vet, up, seed, demo, down, clean"

build:
	mkdir -p bin
	go build -o bin/registry-janitor ./cmd/registry-janitor

test:
	go test ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

up:
	docker compose up -d

seed:
	go run ./hack/seed --registry localhost:5000 --insecure

demo:
	go run ./cmd/registry-janitor prune --registry localhost:5000 --insecure --policy examples/policy.yaml --repo demo/api,demo/worker

down:
	docker compose down

clean:
	rm -f bin/registry-janitor
	rmdir bin 2>/dev/null || true
