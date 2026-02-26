MODULE   := github.com/marcus-qen/legator
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: all build-cp build-probe build-ctl test lint clean

all: build-cp build-probe build-ctl

build-cp:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/control-plane ./cmd/control-plane

build-probe:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/probe ./cmd/probe

build-ctl:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/legatorctl ./cmd/legatorctl

# Cross-compile all binaries
build-all: build-cp-all build-probe-all build-ctl-all

build-cp-all:
	GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/control-plane-linux-amd64 ./cmd/control-plane
	GOOS=linux  GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/control-plane-linux-arm64 ./cmd/control-plane

build-probe-all:
	GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/probe-linux-amd64   ./cmd/probe
	GOOS=linux  GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/probe-linux-arm64   ./cmd/probe
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/probe-darwin-arm64  ./cmd/probe

build-ctl-all:
	GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/legatorctl-linux-amd64   ./cmd/legatorctl
	GOOS=linux  GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/legatorctl-linux-arm64   ./cmd/legatorctl
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/legatorctl-darwin-arm64  ./cmd/legatorctl

test:
	go test ./...

lint:
	golangci-lint run ./...

e2e:
	./hack/e2e-test.sh

clean:
	rm -rf bin/
