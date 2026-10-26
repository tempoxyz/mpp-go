package fiberadapter

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"

	fiberfw "github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

type middlewareTestMethod struct{}

func (middlewareTestMethod) Name() string { return "tempo" }

func (middlewareTestMethod) Intents() map[string]server.Intent {
	return map[string]server.Intent{"charge": verifyTestIntent{}}
}

type verifyTestIntent struct{}

func (verifyTestIntent) Name() string { return "charge" }

func (verifyTestIntent) Verify(_ context.Context, _ *mpp.Credential, _ map[string]any) (*mpp.Receipt, error) {
	return mpp.Success("tempo", "0xreceipt"), nil
}

func TestChargeMiddleware_EndToEnd(t *testing.T) {
	t.Parallel()

	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	app := fiberfw.New()

	app.Get("/paid", ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}), func(c *fiberfw.Ctx) error {
		if c.Get("X-Downstream-Error") != "" {
			return c.Status(http.StatusInternalServerError).SendString("failed")
		}
		if c.Get("X-Downstream-Redirect") != "" {
			return c.Redirect("/elsewhere", fiberfw.StatusFound)
		}
		credential := Credential(c)
		receipt := Receipt(c)
		if !assert.Falsef(t, credential == nil || receipt == nil,
			"expected credential and receipt on fiber context") {
			return *new(error)
		}

		ctxCredential := server.CredentialFromContext(c.UserContext())
		ctxReceipt := server.ReceiptFromContext(c.UserContext())
		if !assert.Falsef(t, ctxCredential == nil || ctxReceipt == nil,
			"expected credential and receipt on user context") {
			return *new(error)
		}

		return c.SendString(credential.Source + ":" + receipt.Reference)
	})

	challengeRequest := httptest.NewRequest(http.MethodGet, "/paid", nil)
	challengeResponse, err := app.Test(challengeRequest)
	if !assert.NoErrorf(t, err,
		"app.Test() error = %v", err) {
		return
	}
	if !assert.Equalf(t, http.StatusPaymentRequired, challengeResponse.StatusCode,
		"challenge status = %d, want %d", challengeResponse.StatusCode, http.StatusPaymentRequired) {
		return
	}

	challenge, err := mpp.ParseChallenge(challengeResponse.Header.Get("WWW-Authenticate"))
	if !assert.NoErrorf(t, err,
		"ParseChallenge() error = %v", err) {
		return
	}

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}

	paidRequest := httptest.NewRequest(http.MethodGet, "/paid", nil)
	paidRequest.Header.Set("Authorization", mpptest.FormatCredential(credential))
	paidResponse, err := app.Test(paidRequest)
	if !assert.NoErrorf(t, err,
		"app.Test() error = %v", err) {
		return
	}
	if !assert.Equalf(t, http.StatusOK, paidResponse.StatusCode,
		"paid status = %d, want %d", paidResponse.StatusCode, http.StatusOK) {
		return
	}

	receipt, err := mpp.ParseReceipt(paidResponse.Header.Get("Payment-Receipt"))
	if !assert.NoErrorf(t, err,
		"ParseReceipt() error = %v", err) {
		return
	}
	if !assert.Equalf(t, "0xreceipt", receipt.Reference,
		"receipt reference = %q, want %q", receipt.Reference, "0xreceipt") {
		return
	}

	body, err := io.ReadAll(paidResponse.Body)
	if !assert.NoErrorf(t, err,
		"ReadAll() error = %v", err) {
		return
	}
	{

		got := string(body)
		if !assert.Equalf(t, "did:key:z6Mkrdemo:0xreceipt", got,
			"response body = %q, want %q", got, "did:key:z6Mkrdemo:0xreceipt") {
			return
		}
	}

	failedRequest := httptest.NewRequest(http.MethodGet, "/paid", nil)
	failedRequest.Header.Set("Authorization", mpptest.FormatCredential(credential))
	failedRequest.Header.Set("X-Downstream-Error", "true")
	failedResponse, err := app.Test(failedRequest)
	require.NoError(t, err)
	defer failedResponse.Body.Close()
	require.Equal(t, http.StatusInternalServerError, failedResponse.StatusCode)
	assert.Empty(t, failedResponse.Header.Get(mpp.HeaderPaymentReceipt))

	redirectRequest := httptest.NewRequest(http.MethodGet, "/paid", nil)
	redirectRequest.Header.Set("Authorization", mpptest.FormatCredential(credential))
	redirectRequest.Header.Set("X-Downstream-Redirect", "true")
	redirectResponse, err := app.Test(redirectRequest)
	require.NoError(t, err)
	defer redirectResponse.Body.Close()
	require.Equal(t, http.StatusFound, redirectResponse.StatusCode)
	assert.Equal(t, "/elsewhere", redirectResponse.Header.Get("Location"))
	assert.Empty(t, redirectResponse.Header.Get(mpp.HeaderPaymentReceipt))
}

func TestChargeMiddlewareAutoScopesRouteResourceAndQuery(t *testing.T) {
	t.Parallel()

	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	app := fiberfw.New()
	app.Get("/paid/:id", ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}), func(c *fiberfw.Ctx) error {
		return c.SendString("paid")
	})

	challengeRequest := httptest.NewRequest(http.MethodGet, "/paid/one?view=full", nil)
	challengeResponse, err := app.Test(challengeRequest)
	require.NoError(t, err)
	defer challengeResponse.Body.Close()
	require.Equal(t, http.StatusPaymentRequired, challengeResponse.StatusCode)

	challenge, err := mpp.ParseChallenge(challengeResponse.Header.Get("WWW-Authenticate"))
	require.NoError(t, err)
	scope, ok := challenge.Request["_mppx_scope"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "/paid/:id", scope["route"])
	assert.Equal(t, "/paid/one", scope["resource"])
	assert.Equal(t, "view=full", scope["query"])

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}
	paidRequest := httptest.NewRequest(http.MethodGet, "/paid/two?view=full", nil)
	paidRequest.Header.Set("Authorization", mpptest.FormatCredential(credential))
	paidResponse, err := app.Test(paidRequest)
	require.NoError(t, err)
	defer paidResponse.Body.Close()

	assert.NotEqual(t, http.StatusOK, paidResponse.StatusCode)
	assert.Empty(t, paidResponse.Header.Get("Payment-Receipt"))
}

func TestChargeMiddlewareRejectsTamperedRequestBodyDigest(t *testing.T) {
	t.Parallel()

	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	app := fiberfw.New()

	app.Post("/paid", ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}), func(c *fiberfw.Ctx) error {
		return c.SendString("paid")
	})

	const originalBody = `{"query":"paid"}`
	challengeRequest := httptest.NewRequest(http.MethodPost, "/paid", strings.NewReader(originalBody))
	challengeResponse, err := app.Test(challengeRequest)
	require.NoError(t, err)
	defer challengeResponse.Body.Close()
	require.Equal(t, http.StatusPaymentRequired, challengeResponse.StatusCode)

	challenge, err := mpp.ParseChallenge(challengeResponse.Header.Get("WWW-Authenticate"))
	require.NoError(t, err)
	assert.Equal(t, mpp.ComputeBodyDigest([]byte(originalBody)), challenge.Digest)

	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}
	paidRequest := httptest.NewRequest(http.MethodPost, "/paid", strings.NewReader(`{"query":"tampered"}`))
	paidRequest.Header.Set("Authorization", mpptest.FormatCredential(credential))
	paidResponse, err := app.Test(paidRequest)
	require.NoError(t, err)
	defer paidResponse.Body.Close()

	assert.Equal(t, http.StatusPaymentRequired, paidResponse.StatusCode)
}

func TestChargeMiddlewarePreservesVerifiedRequestBody(t *testing.T) {
	t.Parallel()

	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	app := fiberfw.New()

	app.Post("/paid", ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}), func(c *fiberfw.Ctx) error {
		return c.SendString(string(c.Body()))
	})

	const originalBody = `{"query":"paid"}`
	challengeRequest := httptest.NewRequest(http.MethodPost, "/paid", strings.NewReader(originalBody))
	challengeResponse, err := app.Test(challengeRequest)
	require.NoError(t, err)
	defer challengeResponse.Body.Close()
	require.Equal(t, http.StatusPaymentRequired, challengeResponse.StatusCode)

	challenge, err := mpp.ParseChallenge(challengeResponse.Header.Get("WWW-Authenticate"))
	require.NoError(t, err)
	credential := &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Source:    "did:key:z6Mkrdemo",
		Payload:   map[string]any{"type": "hash", "hash": "0xabc123"},
	}
	paidRequest := httptest.NewRequest(http.MethodPost, "/paid", strings.NewReader(originalBody))
	paidRequest.Header.Set("Authorization", mpptest.FormatCredential(credential))
	paidResponse, err := app.Test(paidRequest)
	require.NoError(t, err)
	defer paidResponse.Body.Close()
	require.Equal(t, http.StatusOK, paidResponse.StatusCode)

	body, err := io.ReadAll(paidResponse.Body)
	require.NoError(t, err)
	assert.Equal(t, originalBody, string(body))
}

func TestChargeMiddlewareRejectsCRLFChallengeDescription(t *testing.T) {
	t.Parallel()

	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	app := fiberfw.New()

	app.Get("/paid", ChargeMiddleware(payment, server.ChargeParams{
		Amount:      "0.50",
		Description: "Line one\r\nLine two",
	}), func(c *fiberfw.Ctx) error {
		require.Fail(t, "handler should not be called")
		return *new(error)
	})

	challengeRequest := httptest.NewRequest(http.MethodGet, "/paid", nil)
	challengeResponse, err := app.Test(challengeRequest)
	if !assert.NoErrorf(t, err,
		"app.Test() error = %v", err) {
		return
	}

	defer challengeResponse.Body.Close()

	require.Equal(t, http.StatusInternalServerError, challengeResponse.StatusCode)
	assert.Empty(t, challengeResponse.Header.Get("WWW-Authenticate"))

	var problem struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.NewDecoder(challengeResponse.Body).Decode(&problem))
	assert.Equal(t, string(mpp.ErrorTypeInternalPayment), problem.Type)
}

func newTestServer(t *testing.T, method server.Method, realm, secretKey string, opts ...server.Option) *server.Mpp {
	t.Helper()
	payment, err := server.New(method, realm, secretKey, opts...)
	require.NoError(t, err)
	return payment
}

func TestOversizedBodyRejectedBeforePayment(t *testing.T) {
	router := fiberfw.New(fiberfw.Config{BodyLimit: server.MaxRequestBodyBytes + 100})
	router.Post("/", ChargeMiddleware(nil, server.ChargeParams{}), func(c *fiberfw.Ctx) error { t.Fatal("handler called"); return nil })
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", server.MaxRequestBodyBytes+1)))
	response, err := router.Test(req)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusRequestEntityTooLarge, response.StatusCode)
}

func TestChargeMiddlewareRejectsEncodedBodiesBeforeDecompression(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err := io.WriteString(writer, strings.Repeat("x", server.MaxRequestBodyBytes+1))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.Less(t, compressed.Len(), server.MaxRequestBodyBytes)
	for _, encoding := range []string{"gzip", "br", "deflate", "gzip, br", "unknown"} {
		t.Run(encoding, func(t *testing.T) {
			app := fiberfw.New()
			app.Post("/", ChargeMiddleware(nil, server.ChargeParams{}), func(*fiberfw.Ctx) error { t.Fatal("handler called"); return nil })
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(compressed.Bytes()))
			req.Header.Set("Content-Encoding", encoding)
			response, err := app.Test(req)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, http.StatusUnsupportedMediaType, response.StatusCode)
		})
	}
}

type countedStream struct{ remaining, read int }

func (r *countedStream) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.remaining)
	clear(p[:n])
	r.remaining -= n
	r.read += n
	return n, nil
}
func TestChargeMiddlewareBoundsStream(t *testing.T) {
	for _, size := range []int{32, server.MaxRequestBodyBytes + 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
			stream := &countedStream{remaining: size}
			app := fiberfw.New()
			app.Use(func(c *fiberfw.Ctx) error { c.Request().SetBodyStream(stream, -1); return c.Next() })
			app.Post("/paid", ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}))
			resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/paid", nil))
			require.NoError(t, err)
			defer resp.Body.Close()
			if size > server.MaxRequestBodyBytes {
				assert.Equal(t, 413, resp.StatusCode)
			} else {
				assert.Equal(t, 402, resp.StatusCode)
			}
			assert.LessOrEqual(t, stream.read, server.MaxRequestBodyBytes+1)
		})
	}
}

func TestWritePaymentErrorHidesUnexpectedErrorText(t *testing.T) {
	t.Parallel()

	app := fiberfw.New()
	app.Get("/", func(c *fiberfw.Ctx) error {
		WritePaymentResponse(c, fmt.Errorf("dial https://rpc.example/secret-key: connection refused"))
		return nil
	})
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusInternalServerError, response.StatusCode)
	assert.Contains(t, string(body), string(mpp.ErrorTypeInternalPayment))
	assert.NotContains(t, string(body), "secret-key")
}

func TestChargeMiddlewareRespectsCallerMppxScope(t *testing.T) {
	t.Parallel()

	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	app := fiberfw.New()
	app.Get("/paid/:id", ChargeMiddleware(payment, server.ChargeParams{
		Amount:    "0.50",
		MppxScope: map[string]string{"route": "custom"},
	}), func(c *fiberfw.Ctx) error { return nil })

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/paid/one?view=full", nil))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
	challenge, err := mpp.ParseChallenge(resp.Header.Get("WWW-Authenticate"))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"route": "custom"}, challenge.Request["_mppx_scope"])
}

func TestChargeMiddlewareCRLFChallengeDescriptionWithMalformedCredential(t *testing.T) {
	t.Parallel()

	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	app := fiberfw.New()
	app.Get("/paid", ChargeMiddleware(payment, server.ChargeParams{
		Amount:      "0.50",
		Description: "Line one\r\nLine two",
	}), func(c *fiberfw.Ctx) error {
		return c.SendString("paid")
	})

	req := httptest.NewRequest(http.MethodGet, "/paid", nil)
	req.Header.Set(mpp.HeaderAuthorization, "Payment garbage")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Empty(t, resp.Header.Get(mpp.HeaderWWWAuthenticate))
	var problem struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&problem))
	assert.Equal(t, string(mpp.ErrorTypeInternalPayment), problem.Type)
}

func TestChargeMiddlewareRejectsOversizedRequestURI(t *testing.T) {
	t.Parallel()

	payment := newTestServer(t, middlewareTestMethod{}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	app := fiberfw.New(fiberfw.Config{ReadBufferSize: 64 << 10})
	app.Get("/paid", ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"}), func(c *fiberfw.Ctx) error {
		return c.SendString("paid")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/paid?q="+strings.Repeat("a", 13*1024), nil))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusRequestURITooLong, resp.StatusCode)
	assert.Empty(t, resp.Header.Get(mpp.HeaderWWWAuthenticate))
}

func TestWritePaymentResponseOmitsRetryChallengesForOutcomeUnknown(t *testing.T) {
	t.Parallel()

	challenge := mpptest.NewChallenge("test-secret-key-minimum-32-byte-secret", "api.example.com", "tempo", "charge", map[string]any{"amount": "1"})
	app := fiberfw.New()
	app.Get("/", func(c *fiberfw.Ctx) error {
		WritePaymentResponse(c, mpp.ErrPaymentOutcomeUnknown(challenge.ID), challenge)
		return nil
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
	assert.Empty(t, resp.Header.Values(mpp.HeaderWWWAuthenticate))
	var problem struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&problem))
	assert.Equal(t, string(mpp.ErrorTypePaymentOutcomeUnknown), problem.Type)
}
