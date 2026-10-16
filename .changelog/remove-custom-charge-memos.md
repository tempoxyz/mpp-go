---
github.com/tempoxyz/mpp-go: minor
---

Remove custom primary Tempo charge memos from server configuration and request parameters. Clients always generate attribution memos bound to the challenge and realm, and servers require that binding for both calldata and receipt validation. Split-specific memos remain supported.
