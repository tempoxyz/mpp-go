---
github.com/tempoxyz/mpp-go: patch
---

Set `Cache-Control: private` on paid responses in the gin, echo, and fiber adapters. Previously these adapters set only `Payment-Receipt`, so behind a CDN/proxy heuristic caching could leak a payer's receipt and paid body to unpaid clients. The net/http middleware already applied this directive; the adapters now match it.
