---
github.com/tempoxyz/mpp-go: patch
---

Fix the client challenge filter so a challenge whose `expires` cannot be parsed is skipped instead of treated as valid, preventing the client from paying a challenge the server is guaranteed to reject.
