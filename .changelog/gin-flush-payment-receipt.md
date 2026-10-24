---
github.com/tempoxyz/mpp-go: patch
---

Keep the `Payment-Receipt` and `Cache-Control: private` headers on gin responses that call `Flush` before writing the body, such as streaming responses.
