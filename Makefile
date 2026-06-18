.PHONY: build test integration-test

build:
	mkdir -p bin
	go build -o bin/audiobooks ./cmd/audiobooks

test:
	go test ./...

integration-test:
	AUDIOBOOK_INTEGRATION=1 go test -v ./internal/build
