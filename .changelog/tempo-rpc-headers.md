---
github.com/tempoxyz/mpp-go: patch
---

Updated tempo-go to v0.8.0, whose `client.WithHeaders` option lets `tempo.NewRPCClientWithOptions` send a Tempo API key in the `tempo-api-key` header to the hosted RPC (`https://api.tempo.xyz/rpc`), as mppx, pympp and mpp-rs allow.
