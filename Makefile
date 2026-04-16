.PHONY: all build_examples clean test test-coverage check fix integration docs help

# Default target
all: check

# Builds all runnable examples
build_examples:
	@mkdir -p bin
	go build -o bin/charge-basic ./examples/charge-basic
	go build -o bin/charge-hash ./examples/charge-hash
	go build -o bin/charge-fee-payer ./examples/charge-fee-payer

# Cleans generated artifacts
clean:
	go clean
	rm -rf bin/
	rm -f cover.out cover.html coverage.out

# Run unit tests only (integration tests use a build tag and are excluded)
test:
	go test ./...

# Run unit tests with coverage
test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o cover.html

# Run formatting, vet, module verification, and unit tests
check:
	go mod verify
	test -z "$$(gofmt -l .)" || (echo "Code needs formatting. Run 'make fix'" && gofmt -l . && exit 1)
	go vet ./...
	go test ./...

# Format code and tidy dependencies
fix:
	gofmt -s -w .
	go mod tidy

# Run integration tests against the local Tempo devnet by default
integration:
	@TEMPO_RPC_URL=$${TEMPO_RPC_URL:-http://localhost:8545} go test -tags=integration -run TestIntegration -timeout=5m ./tests

# Start a local godoc server for this module
docs:
	which godoc > /dev/null || (echo "Installing godoc..." && go install golang.org/x/tools/cmd/godoc@latest)
	echo "Documentation available at http://localhost:6060/pkg/github.com/tempoxyz/mpp-go/"
	echo "Press Ctrl+C to stop the server"
	@godoc -http=:6060

# Show available targets
help:
	@echo "Available targets:"
	@echo "  build_examples  Build the example binaries into ./bin"
	@echo "  clean           Remove build artifacts and coverage files"
	@echo "  test            Run unit tests"
	@echo "  test-coverage   Run unit tests with coverage report"
	@echo "  check           Run formatting checks, vet, and tests"
	@echo "  fix             Format code and tidy modules"
	@echo "  integration     Run integration tests against TEMPO_RPC_URL"
	@echo "  docs            Start a local godoc server"
