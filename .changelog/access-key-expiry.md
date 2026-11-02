---
github.com/tempoxyz/mpp-go: patch
---

Fixed zero-amount proofs signed by an access key whose AccountKeychain entry has an expiry of 0 being accepted: as in mppx, an access key authorizes a proof only while it is not revoked and its expiry is in the future.

