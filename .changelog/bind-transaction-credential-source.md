---
github.com/tempoxyz/mpp-go: patch
---

Tempo charge transaction credentials now bind a claimed `source` to the chain the challenge resolves to (the RPC's chain when the challenge has no `chainId`), in addition to the transaction sender.
