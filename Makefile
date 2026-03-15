.PHONY: build install test lint

build:
	go build -o cerberus ./cmd/cerberus

install:
	go install ./cmd/cerberus

test:
	go test ./...

lint:
	go vet ./...
