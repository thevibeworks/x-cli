BINARY      := x
PKG         := github.com/thevibeworks/x-cli
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X $(PKG)/internal/version.Version=$(VERSION)
GOFLAGS     := -trimpath

.PHONY: all build install tidy test test-race cover lint vet clean run ci

all: build

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/x

install:
	CGO_ENABLED=0 go install $(GOFLAGS) -ldflags '$(LDFLAGS)' ./cmd/x

tidy:
	go mod tidy

test:
	go test -count=1 ./...

test-race:
	go test -race -count=1 ./...

cover:
	go test -race -count=1 -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -20

lint: vet

vet:
	go vet ./...

clean:
	rm -rf bin/ dist/ coverage.out

run: build
	./bin/$(BINARY)

ci: vet test-race build
