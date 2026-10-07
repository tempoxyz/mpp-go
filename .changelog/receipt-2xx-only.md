---
github.com/tempoxyz/mpp-go: patch
---

Omit `Payment-Receipt` from redirect (3xx) downstream responses. #153 already withheld receipts for 4xx/5xx; net/http, Gin, Echo, and Fiber now attach receipts only for 2xx, matching mpp-rs #399 and closing the remaining gap in #146.
