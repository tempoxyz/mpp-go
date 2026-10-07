package server

import (
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type countingBody struct {
	read   int
	closed bool
}

func (b *countingBody) Read(p []byte) (int, error) { b.read += len(p); return len(p), nil }
func (b *countingBody) Close() error               { b.closed = true; return nil }

func TestRequestBodyLimit(t *testing.T) {
	for _, size := range []int{0, MaxRequestBodyBytes, MaxRequestBodyBytes + 1} {
		for _, knownLength := range []bool{false, true} {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", size)))
			if !knownLength {
				r.ContentLength = -1
			}
			body, err := ReadRequestBody(r)
			if size > MaxRequestBodyBytes {
				require.Error(t, err)
				require.Nil(t, body)
				require.Equal(t, http.StatusRequestEntityTooLarge, RequestBodyError(err).Status)
			} else {
				require.NoError(t, err)
				restored, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.Equal(t, size, len(body))
				require.Equal(t, body, restored)
			}
		}
	}
}

func TestRequestBodyStopsReadingUnboundedStream(t *testing.T) {
	body := &countingBody{}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Body = body
	r.ContentLength = -1
	_, err := ReadRequestBody(r)
	require.Error(t, err)
	require.Equal(t, MaxRequestBodyBytes+1, body.read)
	require.True(t, body.closed)
}

func TestOversizedBodyRejectedBeforePayment(t *testing.T) {
	handler := ChargeMiddleware(nil, ChargeParams{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler called") }))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", MaxRequestBodyBytes+1)))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}
