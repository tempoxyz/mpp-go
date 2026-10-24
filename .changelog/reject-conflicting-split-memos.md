---
github.com/tempoxyz/mpp-go: patch
---

Reject Tempo charge payments whose split transfers carry an attribution memo bound to a different challenge or realm. Previously a split without a configured memo accepted any memo, so one split payment could carry attribution for two challenges and settle both once the first replay marker expired. Plain transfers and non-attribution memos are still accepted on splits.
