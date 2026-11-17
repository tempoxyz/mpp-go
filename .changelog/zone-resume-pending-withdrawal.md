---
github.com/tempoxyz/mpp-go: patch
---

Zone clients resumed a pending withdrawal instead of withdrawing again and refused expired or cross-origin challenges, as mppx does. Servers granted `ZoneCredentialGrace` only to challenges carrying the `_mppx_expiry_grace` marker and rejected it with a Relay.
