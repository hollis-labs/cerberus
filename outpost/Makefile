VERSION ?= dev

.PHONY: build test
build:
	go build -trimpath -ldflags '-X main.version=$(VERSION)' -o bin/outpost ./cmd/outpost

test:
	go test -race ./...
