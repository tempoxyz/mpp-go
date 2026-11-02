---
github.com/tempoxyz/mpp-go: patch
---

Tempo charge verification now matches mppx for two zero-amount proof cases. A legacy Keychain V1 proof envelope (`0x03 || account || inner`), whose inner signature signs the proof hash itself, is accepted from an active access key; previously only Keychain V2 envelopes were. A zero-amount challenge is paid only with a proof credential: a hash or transaction credential is now rejected even without fee sponsorship, as mppx's `Zero-amount challenges require a proof credential`.
