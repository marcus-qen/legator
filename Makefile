MODULE   := github.com/marcus-qen/legator
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: all build-cp build-probe test lint clean

all: build-cp build-probe

build-cp:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/control-plane ./cmd/control-plane

build-probe:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/probe ./cmd/probe

# Cross-compile probe for common targets
build-probe-all:
	GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/probe-linux-amd64   ./cmd/probe
	GOOS=linux  GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/probe-linux-arm64   ./cmd/probe
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/probe-darwin-arm64  ./cmd/probe

test:
	go test ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/
