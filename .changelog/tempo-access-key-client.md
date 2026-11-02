---
github.com/tempoxyz/mpp-go: patch
---

Added `chargeclient.Config.AccessKey`: the Tempo charge client can pay as a Tempo access key on behalf of a root account, as an mppx client with a viem access-key account does. Transactions carry Keychain V2 signatures and, until the AccountKeychain reports the key authorized, the root account's signed `KeyAuthorization`; zero-amount proofs are signed with a Keychain V2 envelope naming the root account. The root account pays and is the credential source.
