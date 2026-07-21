---
github.com/tempoxyz/mpp-go: patch
---

Reject a negative `decimals` in `ParseUnits` (and thus `TransformUnits`) instead of panicking on the fractional-part slice.
