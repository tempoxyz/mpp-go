---
github.com/tempoxyz/mpp-go: patch
---

Reject a bare `"."` amount instead of parsing it as zero. `ParseUnits(".", d)` returned `0` and `NormalizeChargeRequest` normalized `Amount: "."` to `"0"`, turning a charge into a zero-amount one that a proof credential satisfies without any transfer.
