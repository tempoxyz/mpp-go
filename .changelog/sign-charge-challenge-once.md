---
github.com/tempoxyz/mpp-go: patch
---

Shared one in-flight signing between concurrent Tempo charge `CreateCredential` calls for the same challenge, so they cannot produce two independently settleable transactions.
