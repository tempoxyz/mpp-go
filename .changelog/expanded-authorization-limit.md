---
github.com/tempoxyz/mpp-go: patch
---

Raised the Authorization and Payment credential size limit from 16 KiB to 64 KiB, matching mppx. A credential echoes its challenge, so credentials for challenges with large `request` or `opaque` values were rejected even though mppx accepts them. Oversized values are still rejected before scheme extraction and decoding. Challenge and receipt limits are unchanged.
