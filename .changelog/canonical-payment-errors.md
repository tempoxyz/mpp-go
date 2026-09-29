---
github.com/tempoxyz/mpp-go: patch
---

Use canonical problem URIs for invalid-payload and bad-request, and return 402 with a retry challenge for invalid credential payloads. Preserve wrapped payment error statuses and problem details during verification and HTTP response serialization, including Fiber.
