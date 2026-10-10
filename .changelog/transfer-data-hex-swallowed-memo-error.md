---
github.com/tempoxyz/mpp-go: patch
---

Return the error from `EncodeTransferWithMemo` instead of discarding it in `transferDataHex`. A malformed memo (not exactly 32 bytes of hex) previously caused `transferDataHex` to silently return an empty calldata string, which the client would then sign and return as a real, broadcast-ready transaction that called the token contract with no data at all instead of the intended `transferWithMemo` call. `ChargeRequest` is an exported struct that can be constructed without going through `NormalizeChargeRequest`, so a caller-supplied or otherwise unvalidated memo could reach this path directly.
