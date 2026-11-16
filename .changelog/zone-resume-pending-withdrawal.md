---
github.com/tempoxyz/mpp-go: patch
---

Zone clients reuse a late credential only for the challenge it paid, block other challenges with the same terms until its redemption window ends, refuse an expired challenge before paying and resume only under HTTP(S) origins. Servers grant ZoneCredentialGrace only to challenges carrying the HMAC-bound `_mppx_expiry_grace` marker, hold zone replay claims through the full grace and reject the grace with a Relay, as mppx does.
