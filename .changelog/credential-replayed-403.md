---
github.com/tempoxyz/mpp-go: patch
---

Added `mpp.ErrCredentialReplayed`, a 403 `verification-failed` error, and used it for Tempo charge credentials whose payment was already used (replayed transaction hashes and proofs, and sponsored claims held by another request). Servers answer it without a `WWW-Authenticate` challenge, so auto-paying clients do not pay again. Replayed transaction credentials get this error before any chain-state simulation, so a payment that already moved the payer's funds is not answered with a fresh challenge.
