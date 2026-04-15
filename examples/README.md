# Examples

Each example directory is a runnable end-to-end demo with the same layout:

- `server.go` starts a paid HTTP handler using the generic `pkg/server` helpers
- `client.go` configures the generic `pkg/client` retry flow with the Tempo method
- `main.go` starts the server and then calls it as the client

Start the local Tempo devnet before running them:

```bash
docker compose up -d
```

The examples use `mppclient` and `mppserver` for the generic HTTP 402 packages.
Each file aliases the Tempo charge package to `charge`, since it only uses one
side of the Tempo flow at a time.

Run any example directly from the repo root:

```bash
go run ./examples/charge-basic
go run ./examples/charge-hash
go run ./examples/charge-fee-payer
```

Set `TEMPO_RPC_URL` to point at a different node if you are not using the
local dockerized devnet.
