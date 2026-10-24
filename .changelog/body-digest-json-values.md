---
github.com/tempoxyz/mpp-go: patch
---

`mpp.BodyDigest` no longer panics on JSON-compatible bodies such as slices, typed structs or `json.RawMessage`. A `json.RawMessage` or a value of a named string or byte-slice type is digested as is, and other values as their `json.Marshal` output. An `io.Reader` such as `http.Request.Body` still panics, since its JSON form would bind every request to the same digest.
