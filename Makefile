VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/lestex/vpncli/internal/cli.version=$(VERSION)

.PHONY: build test vet lint tidy fmt check dist clean install

# CGO stays off for anything shipped: the SQLite driver is pure Go, so the
# binary is static and cross-compiles without a C toolchain. It is set per
# target rather than globally because `test` needs the opposite - the race
# detector requires cgo.
build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o vpncli ./cmd/vpncli

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

# What CI checks: a dependency that is imported but still marked indirect is a
# red build, and nothing else in `check` notices - build and test are happy
# either way.
tidy:
	go mod tidy
	git diff --exit-code -- go.mod go.sum

fmt:
	gofmt -l -w .

check: vet lint tidy test

# Cross-compiled release archives, identical to what the release workflow
# publishes - so a tag can be rehearsed locally before it is pushed.
dist:
	VERSION=$(VERSION) scripts/release.sh

install:
	CGO_ENABLED=0 go install -ldflags "$(LDFLAGS)" ./cmd/vpncli

clean:
	rm -rf vpncli dist
	go clean -testcache
