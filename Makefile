GO ?= /usr/local/go/bin/go
GOLANGCI_LINT ?= golangci-lint
BIN_DIR ?= bin

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
REGISTRY ?= harbor.lab.k-dev.uk/legator
IMAGE_TAG ?= $(VERSION)

LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build build-cp build-probe build-ctl build-all build-cp-all build-probe-all build-ctl-all release-build test lint e2e docker-probe docker-push-probe clean

build: build-cp build-probe build-ctl

build-cp:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/control-plane ./cmd/control-plane

build-probe:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/probe ./cmd/probe

build-ctl:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/legatorctl ./cmd/legatorctl

build-all: build-cp-all build-probe-all build-ctl-all

build-cp-all:
	mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/legator-control-plane-linux-amd64 ./cmd/control-plane
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/legator-control-plane-linux-arm64 ./cmd/control-plane

build-probe-all:
	mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/legator-probe-linux-amd64 ./cmd/probe
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/legator-probe-linux-arm64 ./cmd/probe
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/legator-probe-darwin-arm64 ./cmd/probe

build-ctl-all:
	mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/legator-ctl-linux-amd64 ./cmd/legatorctl
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/legator-ctl-linux-arm64 ./cmd/legatorctl
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/legator-ctl-darwin-arm64 ./cmd/legatorctl

release-build: build-all

test:
	$(GO) test ./... -count=1

lint:
	$(GOLANGCI_LINT) run ./...

e2e:
	bash hack/e2e-test.sh

docker-probe:
	docker build -f Dockerfile.probe \
		-t $(REGISTRY)/probe:$(IMAGE_TAG) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) .

docker-push-probe:
	docker push $(REGISTRY)/probe:$(IMAGE_TAG)

clean:
	rm -rf $(BIN_DIR)
