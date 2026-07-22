---
github.com/tempoxyz/mpp-go: patch
---

Decode a bare `0x` (and empty) hex quantity as zero in `ParseHexUint64`, matching `ParseHexBigInt` so both JSON-RPC integer decoders agree on zero-value forms returned by lenient nodes.
