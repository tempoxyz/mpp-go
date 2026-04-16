# Examples

The repo has two kinds of examples:

- `charge-*` directories are one-command end-to-end demos that start an in-process server and immediately call it with the client
- `basic/server`, `basic/client`, and each `charge-*/server` / `charge-*/client` pair are separate-process examples you can run in different terminals, similar to the `mpp-rs` sample layout
- `chi/server`, `gin/server`, and `echo/server` show how to plug MPP into common Go web frameworks

Each `charge-*` example directory is a runnable end-to-end demo with the same layout:

- `server.go` starts a paid HTTP handler using the generic `pkg/server` helpers
- `client.go` configures the generic `pkg/client` retry flow with the Tempo method
- `main.go` starts the server and then calls it as the client

Start the local Tempo devnet before running them:

```bash
docker compose up -d
```

The examples import `pkg/client` and `pkg/server` by their bare package names.
Each file aliases the Tempo package it uses to `charge`, since it only uses one
side of the Tempo flow at a time.

Run any example directly from the repo root:

```bash
go run ./examples/charge-basic
go run ./examples/charge-hash
go run ./examples/charge-fee-payer
go run ./examples/basic/server
go run ./examples/basic/client
go run ./examples/chi/server
go run ./examples/gin/server
go run ./examples/echo/server
go run ./examples/charge-basic/server
go run ./examples/charge-basic/client
go run ./examples/charge-hash/server
go run ./examples/charge-hash/client
go run ./examples/charge-fee-payer/server
go run ./examples/charge-fee-payer/client
```

Set `TEMPO_RPC_URL` to point at a different node if you are not using the
local dockerized devnet.

`server.ChargeMiddleware` already works with `net/http` and routers built on
top of it, such as Chi. The Gin and Echo examples use the dedicated adapter
packages under `pkg/server/gin` and `pkg/server/echo`.
