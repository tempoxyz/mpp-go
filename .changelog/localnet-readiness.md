---
github.com/tempoxyz/mpp-go: patch
---

The examples' `devnet.WaitForRPC` now waits until the local devnet builds blocks at the current time, not only until it answers `eth_chainId`, so funding a freshly started devnet no longer fails with `valid_before ... too far in the future`.
