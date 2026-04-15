# Charge Basic

Tempo charge example in both supported layouts:

- `go run ./examples/charge-basic` runs the existing one-command end-to-end demo
- `go run ./examples/charge-basic/server` and `go run ./examples/charge-basic/client` run the same flow in separate terminals

## Running

Start the local devnet first:

```bash
docker compose up -d
```

### 1. Start the server

```bash
go run ./examples/charge-basic/server
```

### 2. Run the client

```bash
go run ./examples/charge-basic/client
```
