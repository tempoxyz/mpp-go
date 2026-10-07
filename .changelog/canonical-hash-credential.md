---
github.com/tempoxyz/mpp-go: patch
---

Canonicalize the transaction hash in `hash` credentials before looking up the receipt and reserving the replay-protection key, and reject values that are not 32-byte hex. Nodes resolve `0x`-prefixed, unprefixed and mixed-case spellings to the same transaction, so a paid hash could previously be redeemed once per spelling.
