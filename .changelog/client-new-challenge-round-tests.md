---
github.com/tempoxyz/mpp-go: patch
---

Added regression tests for a 402 that answers a zero-amount proof with a credit_insufficient problem offering only the real charge: the HTTP client and MCP client pay the new challenge, never pay a challenge twice and stop at their retry limit, as in mppx's client Fetch retry rounds.
