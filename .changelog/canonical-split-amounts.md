---
github.com/tempoxyz/mpp-go: patch
---

Canonicalize Tempo split amounts in `tempo.ParseChargeRequest`, as the total amount already is. A split amount with leading zeros or surrounding whitespace (such as `"0100"` or `" 50 "`) was kept as written, so the server rejected a matching transfer because it compares split amounts as decimal strings, and the client failed to build the transfer for a whitespace-padded amount.
