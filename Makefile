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

# Fails if any .go file is gitignored (unanchored patterns like `standup`
# once swallowed cmd/standup/ and CI checked out a tree with no entrypoint).
ignored-go:
	@ignored=$$(git ls-files --others --ignored --exclude-standard | grep '\.go$$' || true); \
	if [ -n "$$ignored" ]; then \
		echo "gitignored .go files:"; echo "$$ignored"; \
		echo "anchor the .gitignore pattern (e.g. /standup, not standup)"; exit 1; fi

lint: fmt vet cyclo ineffassign golangci deadcode

verify: lint test ignored-go
