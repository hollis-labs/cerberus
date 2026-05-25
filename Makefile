.PHONY: all build install test lint ui-build ui-dev release-beta clean

all: ui-build build

ui-build:
	cd web && npm install && npm run build

ui-dev:
	cd web && npm install && npm run dev

build:
	go build -o cerberus ./cmd/cerberus

release-beta:
	./scripts/release-beta.sh

install:
	go install ./cmd/cerberus

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -f cerberus
	rm -rf web/node_modules
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
