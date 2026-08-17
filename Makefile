VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/lestex/vpncli/internal/cli.version=$(VERSION)

# CGO stays off: the SQLite driver is pure Go, so the binary is static and
# cross-compiles without a C toolchain.
export CGO_ENABLED = 0

.PHONY: build test vet lint fmt check clean install

build:
	go build -ldflags "$(LDFLAGS)" -o vpncli .

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -l -w .

check: vet lint test

install:
	go install -ldflags "$(LDFLAGS)" .

clean:
	rm -f vpncli
	go clean -testcache
