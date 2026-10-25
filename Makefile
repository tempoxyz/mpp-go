.PHONY: all build_examples clean test test-coverage check fix lint lint-all vuln mod-check fuzz integration docs help

# Default target
all: check

# Builds every example package to catch compile drift in CI and locally.
build_examples:
	go build ./examples/...

# Cleans generated artifacts
clean:
	go clean
	rm -rf bin/
	rm -f cover.out cover.html coverage.out

# Run unit tests only (integration tests use a build tag and are excluded),
# with the race detector and in random order, as CI does
test:
	go test -race -shuffle=on -count=1 ./...

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

# Report golangci-lint issues introduced since the merge base with LINT_BASE
# (as CI does); install: https://golangci-lint.run/docs/welcome/install/
LINT_BASE ?= origin/main
lint:
	golangci-lint run --new-from-merge-base=$(LINT_BASE) ./...

# Report every golangci-lint issue, including pre-existing ones
lint-all:
	golangci-lint run ./...

# Scan for known vulnerabilities reachable from this module
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

# Check go.mod and go.sum are tidy and dependencies match go.sum
mod-check:
	go mod tidy -diff
	go mod verify

# Run every Fuzz* target under pkg/ for FUZZTIME each. FUZZTIME reaches the
# recipe unexpanded through the environment (make would expand an exported
# FUZZTIME, as it exports command-line variables) and must be a count with a unit
# (20s, 5m, 1h or 1000x), so a value can't run make functions or shell code.
FUZZTIME ?= 20s
unexport FUZZTIME
fuzz: export MPP_FUZZTIME := $(value FUZZTIME)
fuzz:
	@set -e; \
	if ! printf '%s' "$$MPP_FUZZTIME" | grep -Eqx '[0-9]+(s|m|h|x)'; then \
		echo "FUZZTIME must be a count with a unit such as 20s, 5m, 1h or 1000x" >&2; exit 2; \
	fi; \
	pkgs=$$(go list ./pkg/...); \
	found=0; \
	for pkg in $$pkgs; do \
		listed=$$(go test -list '^Fuzz' "$$pkg"); \
		for target in $$(printf '%s\n' "$$listed" | grep '^Fuzz' || true); do \
			found=$$((found + 1)); \
			echo "==> $$pkg $$target"; \
			go test -run='^$$' -fuzz="^$$target\$$" -fuzztime="$$MPP_FUZZTIME" "$$pkg"; \
		done; \
	done; \
	if [ "$$found" -eq 0 ]; then echo "no Fuzz* targets found under ./pkg" >&2; exit 1; fi

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
	@echo "  build_examples  Build all example packages (compile check)"
	@echo "  clean           Remove build artifacts and coverage files"
	@echo "  test            Run unit tests"
	@echo "  test-coverage   Run unit tests with coverage report"
	@echo "  check           Run formatting checks, vet, and tests"
	@echo "  fix             Format code and tidy modules"
	@echo "  lint            Run golangci-lint on changes since LINT_BASE (origin/main)"
	@echo "  lint-all        Run golangci-lint on the whole module"
	@echo "  vuln            Run govulncheck"
	@echo "  mod-check       Check go.mod/go.sum are tidy and verified"
	@echo "  fuzz            Run each fuzz target for FUZZTIME (default 20s)"
	@echo "  integration     Run integration tests against TEMPO_RPC_URL"
	@echo "  docs            Start a local godoc server"
