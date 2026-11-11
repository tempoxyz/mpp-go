# Basic

A realistic two-process Tempo charge example for `mpp-go`, mirroring the
separate server/client setup from `mpp-rs`.

The server exposes three endpoints:

- `GET /api/health` — Free, returns `{"status":"ok"}`
- `GET /api/ping` — Costs `0.01`, returns `{"pong":true}`
- `GET /api/fortune` — Costs `1.00`, returns a random fortune and payer DID

## Running

Start the local Tempo devnet first:

```bash
docker compose up -d
```

### 1. Start the server

```bash
go run ./examples/basic/server
```

The server listens on `http://localhost:3000`.
It generates and funds a fresh merchant address on startup, similar to the
`mpp-rs` basic example.

### 2. Run the client

In another terminal:

```bash
go run ./examples/basic/client
```

The client automatically handles the 402 flow, pays the challenge, retries,
and prints the returned receipt.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `PRIVATE_KEY` | bundled devnet payer key | Client private key used to pay |
| `BASE_URL` | `http://localhost:3000` | Base URL for the example server |
| `TEMPO_RPC_URL` | `http://127.0.0.1:8545` | Tempo RPC endpoint |
| `MPP_SECRET_KEY` | `basic-example-secret` | Secret key used to sign server challenges |
