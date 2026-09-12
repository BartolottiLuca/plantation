.PHONY: build test lint run

build:
	go build -o plantation ./cmd/plantation

test:
	go test ./... -race

lint:
	golangci-lint run

run:
	go run ./cmd/plantation serve
