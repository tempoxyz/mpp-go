package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gofiber/fiber/v2"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
	echoadapter "github.com/tempoxyz/mpp-go/pkg/server/echo"
	fiberadapter "github.com/tempoxyz/mpp-go/pkg/server/fiber"
	ginadapter "github.com/tempoxyz/mpp-go/pkg/server/gin"
)

type paymentErrorMethod struct{ intent server.Intent }

func (paymentErrorMethod) Name() string { return "tempo" }
func (m paymentErrorMethod) Intents() map[string]server.Intent {
	return map[string]server.Intent{"charge": m.intent}
}

func TestMiddlewarePaymentErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, framework := range []string{"charge", "compose", "gin", "echo", "fiber"} {
		for _, split := range []bool{false, true} {
			for _, wrapped := range []bool{false, true} {
				for _, tc := range []struct {
					name    string
					problem *mpp.PaymentError
					status  int
					uri     string
				}{
					{"invalid payload", mpp.ErrInvalidPayload("payload rejected"), 402, "https://paymentauth.org/problems/invalid-payload"},
					{"bad request", mpp.ErrBadRequest("invalid parameters"), 400, "https://paymentauth.org/problems/bad-request"},
				} {
					t.Run(fmt.Sprintf("%s/split=%t/wrapped=%t/%s", framework, split, wrapped, tc.name), func(t *testing.T) {
						var intentErr error = tc.problem
						if wrapped {
							intentErr = fmt.Errorf("method context: %w", intentErr)
						}
						verifyCalls := 0
						verify := func(context.Context, *mpp.Credential, map[string]any) (*mpp.Receipt, error) {
							verifyCalls++
							if intentErr != nil {
								return nil, intentErr
							}
							return mpp.Success("tempo", "receipt-1"), nil
						}
						hooks := server.IntentHooks{Verify: verify}
						if split {
							hooks = server.IntentHooks{
								Validate: func(context.Context, *mpp.Credential, map[string]any) (*server.Validation, error) {
									verifyCalls++
									return nil, intentErr
								},
								Broadcast: func(context.Context, *mpp.Credential, map[string]any) (*mpp.Receipt, error) {
									return mpp.Success("tempo", "receipt-1"), nil
								},
							}
						}
						intent, err := server.NewIntent("charge", hooks)
						require.NoError(t, err)
						payment, err := server.New(paymentErrorMethod{intent}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
						require.NoError(t, err)
						params := server.ChargeParams{Amount: "0.50"}
						handlerCalls := 0
						next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { handlerCalls++; w.WriteHeader(http.StatusOK) })
						var handler http.Handler
						var app *fiber.App
						switch framework {
						case "charge":
							handler = server.ChargeMiddleware(payment, params)(next)
						case "compose":
							handler = server.ComposeMiddleware(server.ComposeConfig{Mpp: payment, Params: params})(next)
						case "gin":
							router := gin.New()
							router.GET("/paid", ginadapter.ChargeMiddleware(payment, params), func(c *gin.Context) { handlerCalls++; c.Status(http.StatusOK) })
							handler = router
						case "echo":
							router := echo.New()
							router.GET("/paid", func(c echo.Context) error { handlerCalls++; return c.NoContent(http.StatusOK) }, echoadapter.ChargeMiddleware(payment, params))
							handler = router
						case "fiber":
							app = fiber.New()
							app.Get("/paid", fiberadapter.ChargeMiddleware(payment, params), func(c *fiber.Ctx) error { handlerCalls++; return c.SendStatus(http.StatusOK) })
						}
						send := func(authorization string) *http.Response {
							req := httptest.NewRequest(http.MethodGet, "/paid", nil)
							req.Header.Set(mpp.HeaderAuthorization, authorization)
							var resp *http.Response
							if app != nil {
								var err error
								resp, err = app.Test(req)
								require.NoError(t, err)
							} else {
								recorder := httptest.NewRecorder()
								handler.ServeHTTP(recorder, req)
								resp = recorder.Result()
							}
							t.Cleanup(func() { resp.Body.Close() })
							return resp
						}
						initial := send("")
						require.Equal(t, http.StatusPaymentRequired, initial.StatusCode)
						challenge, err := mpp.ParseChallenge(initial.Header.Get(mpp.HeaderWWWAuthenticate))
						require.NoError(t, err)
						credential := challenge.NewCredential(map[string]any{"type": "hash", "hash": "0xabc123"})
						rejected := send(credential.ToAuthorization())
						require.Equal(t, tc.status, rejected.StatusCode)
						assert.Zero(t, handlerCalls)
						assert.Equal(t, 1, verifyCalls)
						assert.Empty(t, rejected.Header.Get(mpp.HeaderPaymentReceipt))
						assert.Equal(t, "application/problem+json", rejected.Header.Get("Content-Type"))
						assert.Equal(t, "no-store", rejected.Header.Get("Cache-Control"))
						var problem mpp.PaymentError
						require.NoError(t, json.NewDecoder(rejected.Body).Decode(&problem))
						assert.Equal(t, tc.uri, string(problem.Type))
						assert.Equal(t, tc.status, problem.Status)
						assert.Equal(t, tc.problem.Detail, problem.Detail)
						assert.Equal(t, tc.problem.Title, problem.Title)
						if tc.status == http.StatusBadRequest {
							assert.Empty(t, rejected.Header.Get(mpp.HeaderWWWAuthenticate))
							return
						}
						retryChallenge, err := mpp.ParseChallenge(rejected.Header.Get(mpp.HeaderWWWAuthenticate))
						require.NoError(t, err)
						assert.True(t, retryChallenge.Verify("test-secret-key-minimum-32-byte-secret", "api.example.com"))
						assert.Equal(t, challenge.Request, retryChallenge.Request)
						intentErr = nil
						retry := send(retryChallenge.NewCredential(map[string]any{"type": "hash", "hash": "0xdef456"}).ToAuthorization())
						assert.Equal(t, http.StatusOK, retry.StatusCode)
						assert.Equal(t, 1, handlerCalls)
						assert.Equal(t, 2, verifyCalls)
						receipt, err := mpp.ParseReceipt(retry.Header.Get(mpp.HeaderPaymentReceipt))
						require.NoError(t, err)
						assert.Equal(t, "receipt-1", receipt.Reference)
					})
				}
			}
		}
	}
}

func TestWriteWrappedPaymentError(t *testing.T) {
	for _, framework := range []string{"http", "fiber"} {
		for _, problem := range []*mpp.PaymentError{mpp.ErrInvalidPayload("payload rejected"), mpp.ErrBadRequest("invalid parameters")} {
			t.Run(framework+"/"+problem.Title, func(t *testing.T) {
				problem.Hint = "try another payload"
				problem.Details = map[string]any{"field": "type"}
				wrapped := fmt.Errorf("method context: %w", problem)
				var resp *http.Response
				if framework == "fiber" {
					app := fiber.New()
					app.Get("/", func(c *fiber.Ctx) error { fiberadapter.WritePaymentError(c, wrapped); return nil })
					var err error
					resp, err = app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
					require.NoError(t, err)
				} else {
					recorder := httptest.NewRecorder()
					server.WritePaymentError(recorder, wrapped)
					resp = recorder.Result()
				}
				defer resp.Body.Close()
				assert.Equal(t, problem.Status, resp.StatusCode)
				var got mpp.PaymentError
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
				assert.Equal(t, *problem, got)
			})
		}
	}
}
