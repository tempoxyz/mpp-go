---
github.com/tempoxyz/mpp-go: patch
---

Stopped automatic MCP replacement payments after a hash credential was sent, and preserved installed access-key restrictions in sponsored transaction simulations.

Exposed the submitted hash Credential and underlying server error for explicit MCP payment reconciliation.

Updated Echo v5 to v5.4.4 and preserved receipt emission after Echo hooks choose the final response status, including buffered gzip responses. Rejected incompatible custom response writers before processing payments. The adapter package path is unchanged; Echo v5 handlers use `*echo.Context`.

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
