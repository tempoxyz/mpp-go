---
github.com/tempoxyz/mpp-go: minor
---

Moved the Echo adapter (`pkg/server/echo`) from Echo v4 to Echo v5: `Credential`, `Receipt` and the middleware handlers now take `*echo.Context`. Echo v4 has unpatched advisories (GO-2026-6872, GO-2026-6875) fixed only in v5. Also upgraded github.com/andybalholm/brotli to v1.2.7 for GO-2026-6888.
