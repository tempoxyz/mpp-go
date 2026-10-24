---
github.com/tempoxyz/mpp-go: patch
---

`mpp.BodyDigest` no longer panics on JSON-compatible bodies such as slices, typed structs or `json.RawMessage`. A `json.RawMessage` is digested as is, and other values as their `json.Marshal` output.
