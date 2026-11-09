// Package echoadapter provides payment middleware for Echo v5.
//
// Applications migrating from Echo v4 must import github.com/labstack/echo/v5
// and change handlers and context-helper arguments to *echo.Context. The
// adapter import path remains github.com/tempoxyz/mpp-go/pkg/server/echo.
// Custom response writers must unwrap to Echo's Response, as required by Echo
// response helpers; the adapter checks this before processing any payment.
package echoadapter
