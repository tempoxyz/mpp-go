---
github.com/tempoxyz/mpp-go: patch
---

Raised the Authorization and Payment credential size limit from 16 KiB to 64 KiB, matching mppx. A credential echoes its challenge, so credentials for challenges with large `request` or `opaque` values were rejected even though mppx accepts them. Oversized values are still rejected before scheme extraction and decoding. Challenge and receipt limits are unchanged.

Upgrade servers before clients. An older mpp-go server rejects credentials over 16 KiB, and a Tempo hash-credential client broadcasts its payment before the server sees the credential, so an upgraded client paying an older server can pay without being served.
