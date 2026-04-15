# Charge Hash

Push-mode Tempo charge example in both supported layouts:

- `go run ./examples/charge-hash` runs the existing one-command end-to-end demo
- `go run ./examples/charge-hash/server` and `go run ./examples/charge-hash/client` run the same flow in separate terminals

## Running

Start the local devnet first:

```bash
docker compose up -d
```

### 1. Start the server

```bash
go run ./examples/charge-hash/server
```

### 2. Run the client

```bash
go run ./examples/charge-hash/client
```
