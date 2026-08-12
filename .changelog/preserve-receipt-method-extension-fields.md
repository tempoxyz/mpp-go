---
github.com/tempoxyz/mpp-go: minor
---

Preserve method-defined top-level extension fields (such as `originTxHash`) when parsing and formatting payment receipts. draft-ietf-httpauth-payment §5.3 allows payment methods to define additional receipt fields, and the canonical mppx schema keeps them across a parse/format round trip; previously any top-level field outside the base set was silently dropped. Such fields are now retained on the new `Receipt.Extensions` map and re-emitted at the top level.
