.PHONY: test build run fmt vet cyclo ineffassign golangci deadcode lint verify

test:
	go test ./...

build:
	go build -o standup ./cmd/standup

run:
	go run ./cmd/standup

fmt:
	gofmt -l -w .

vet:
	go vet ./...

cyclo:
	gocyclo -over 15 .

ineffassign:
	ineffassign ./...

golangci:
	golangci-lint run

deadcode:
	deadcode ./...

lint: fmt vet cyclo ineffassign golangci deadcode

verify: lint test
