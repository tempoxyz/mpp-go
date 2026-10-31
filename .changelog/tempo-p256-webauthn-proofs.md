---
github.com/tempoxyz/mpp-go: patch
---

The Tempo charge server verifies zero-amount proofs signed with P256 and WebAuthn keys, bare or inside a Keychain envelope, as mppx does with ox's SignatureEnvelope, using tempo-go v0.7.0's signer.RecoverEnvelope. Verification matches Tempo's: low-s P256 signatures over the digest (or its SHA-256 for pre-hashed keys), and WebAuthn assertions with user presence, no attested or extension data, type webauthn.get and the digest as challenge. As for secp256k1 access keys, the key must be an active access key of the credential's source. Upgraded tempo-go to v0.7.0, which decodes Keychain V1 and P256 or WebAuthn Keychain inner signatures, so only multisig sender envelopes are still carried as raw bytes.
