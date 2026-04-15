# Examples

Each example directory is a runnable end-to-end demo with the same layout:

- `server.go` starts a paid HTTP handler using the generic `pkg/server` helpers
- `client.go` configures the generic `pkg/client` retry flow with the Tempo method
- `main.go` starts the server and then calls it as the client

The example code uses `mppclient` and `mppserver` for the generic HTTP 402
packages, and the Tempo charge packages import naturally as `chargeclient` and
`chargeserver`.

Run any example directly from the repo root:

```bash
go run ./examples/charge-basic
go run ./examples/charge-hash
go run ./examples/charge-fee-payer
```

All examples use the mock Tempo RPC in [`internal/temposim`](./internal/temposim), so they exercise the full HTTP 402 flow without requiring a local node.
