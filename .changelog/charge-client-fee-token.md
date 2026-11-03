---
github.com/tempoxyz/mpp-go: patch
---

Added `FeeToken` to the Tempo charge client's `Config`, as mppx's charge client `feeToken` option: unsponsored transactions pay their fee in that token instead of the charge currency, so an access key whose spending limits exclude the currency's fee can still pay.
