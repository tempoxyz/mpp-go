# Charge Fee Payer

Sponsored Tempo charge example in both supported layouts:

- `go run ./examples/charge-fee-payer` runs the existing one-command end-to-end demo
- `go run ./examples/charge-fee-payer/server` and `go run ./examples/charge-fee-payer/client` run the same flow in separate terminals

## Running

Start the local devnet first:

```bash
docker compose up -d
```

### 1. Start the server

```bash
go run ./examples/charge-fee-payer/server
```

### 2. Run the client

```bash
go run ./examples/charge-fee-payer/client
```
