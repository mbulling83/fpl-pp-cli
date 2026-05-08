.PHONY: build test lint install clean

build:
	go build -o bin/fpl-pp-cli ./cmd/fpl-pp-cli

test:
	go test ./...

lint:
	golangci-lint run

install:
	go install ./cmd/fpl-pp-cli

clean:
	rm -rf bin/

build-mcp:
	go build -o bin/fpl-pp-mcp ./cmd/fpl-pp-mcp

install-mcp:
	go install ./cmd/fpl-pp-mcp

build-all: build build-mcp
