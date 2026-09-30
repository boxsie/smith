VERSION ?= dev
BINARY  := smith
GOFLAGS := -ldflags "-X github.com/boxsie/smith/internal/cli.Version=$(VERSION)"

.PHONY: build test clean

build:
	go build $(GOFLAGS) -o $(BINARY) ./cmd/smith

test:
	go test ./...

clean:
	rm -f $(BINARY)
