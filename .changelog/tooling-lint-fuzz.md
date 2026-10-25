---
github.com/tempoxyz/mpp-go: patch
---

Raised golang.org/x/net to v0.60.0, golang.org/x/text to v0.42.0 and github.com/quic-go/quic-go to v0.59.1 to fix reachable vulnerabilities reported by govulncheck, and lowered the minimum Go version to 1.26.0 (builds use the go1.26.10 toolchain). Added golangci-lint, govulncheck, API compatibility and nightly fuzzing checks.
