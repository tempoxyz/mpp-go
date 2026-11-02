---
github.com/tempoxyz/mpp-go: minor
---

Aligned Tempo charge proofs and submission modes with mppx:

- The server now pays a zero-amount charge only with a proof credential, sponsored or not (it used to accept a transaction or hash credential for an unsponsored zero-amount charge), and accepts Keychain V1 (type 0x03) proof envelopes, whose inner signature signs the proof digest, alongside Keychain V2.
- A push (hash) credential now pays a sponsored challenge: the client broadcasts the transaction and pays its fees itself. The client no longer refuses `CredentialTypeHash` for sponsored challenges, and the server no longer requires a transaction credential for them.
- Without `Config.CredentialType`, the Tempo client pulls, and pushes when the challenge's `methodDetails.supportedModes` allows only push (mppx uses `supportedModes[0]`).
- Issuing a push-only sponsored charge no longer requires its challenge to expire at least 15 seconds after issuance, since it never uses the fee payer.

