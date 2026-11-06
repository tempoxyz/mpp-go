---
github.com/tempoxyz/mpp-go: minor
---

Stopped automatic MCP replacement payments after a hash credential was sent, and preserved installed access-key restrictions in sponsored transaction simulations.

Updated Brotli to fix excessive allocations when decompressing crafted streams.

Migrated the Echo adapter to `github.com/labstack/echo/v5` v5.4.4, removing the vulnerable v4 dependency. This breaks Echo v4 handler and middleware compatibility: import Echo v5 and change handlers and context helpers to use `*echo.Context`. The adapter package path is unchanged. Updated the example and integration tests to v5.

```go
import (
    "net/http"
    "github.com/labstack/echo/v5"
    "github.com/tempoxyz/mpp-go/pkg/server"
    echoadapter "github.com/tempoxyz/mpp-go/pkg/server/echo"
)

// payment is your configured *server.Mpp.
e := echo.New()
e.GET("/paid", func(c *echo.Context) error {
    return c.String(http.StatusOK, "paid content")
}, echoadapter.ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}))
```
