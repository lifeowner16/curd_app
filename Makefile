.PHONY: run test build fmt

run:
	go run ./cmd/api

test:
	go test -race ./...
	go vet ./...

build:
	CGO_ENABLED=0 go build -trimpath -o bin/api ./cmd/api

fmt:
	gofmt -w cmd internal
