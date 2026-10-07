---
github.com/tempoxyz/mpp-go: patch
---

Limit payment middleware request bodies to 4 MiB and return HTTP 413 for larger fixed-length or chunked bodies. Fiber applications should configure their transport BodyLimit at or below this bound because Fiber buffers before middleware. Configure HTTP read timeouts as well.

Fiber payment middleware rejects encoded bodies with HTTP 415 before decompression. Oversized bodies use a generic HTTP 413 problem response.
