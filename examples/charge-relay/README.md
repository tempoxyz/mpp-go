# Charge Relay

A one-command Go API and client that accept pathUSD on Tempo Moderato through
Tempo API's MPP relay. It mirrors the MPPX relay example: the server issues a
pull-mode charge for `/api/photo`, the client signs it, and the relay validates
and broadcasts the credential.

## Setup

Create a Tempo API key with the `mpp:write` scope and provide it only to the
server process:

```bash
export TEMPO_API_KEY=tempo:sk:...
export TEMPO_API_URL=https://api.tempo.xyz
export MPP_SECRET_KEY=$(openssl rand -base64 32)
go run ./examples/charge-relay
```

`TEMPO_API_URL` can target a compatible self-hosted or preview Tempo API.
`MPP_SECRET_KEY` protects the server-issued challenges; the example has a
development-only default. The example creates fresh payer and recipient
accounts, funds the payer through the Moderato faucet, starts an in-process
HTTP server, validates a credential without broadcasting, submits that same
credential to `/api/photo`, and prints the relay receipt reference.

## Routes

| Route         | Description             |
| ------------- | ----------------------- |
| `/api/photo`  | Payment-gated image URL |
| `/api/health` | Free health check       |

## Relay configuration

```go
method, err := charge.MethodFromConfig(charge.Config{
	ChainID:   tempotx.ChainIdModerato,
	Currency:  tempo.DefaultCurrencyForChain(tempotx.ChainIdModerato),
	Recipient: recipient,
	Relay: &charge.RelayConfig{
		APIKey:     os.Getenv("TEMPO_API_KEY"),
		APIBaseURL: os.Getenv("TEMPO_API_URL"),
	},
	SupportedModes: []tempo.ChargeMode{tempo.ChargeModePull},
})
```

The relay registers separate validation and broadcast hooks through
`server.IntentHooks`. `payment.ValidateCredential` calls only the relay
validation endpoint, while `payment.BroadcastCredential` re-validates and then
calls the broadcast endpoint. Paid HTTP routes use that same split lifecycle
automatically. `payment.VerifyCredential` remains a backwards-compatible alias
for the mutating broadcast path.

The relay broadcasts pull credentials and recognizes push credentials as
already broadcast transactions, returning their receipts without sending them
again. Relay failures expose only safe machine-readable codes.
