package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

// nilPayloadMethod returns a Credential mpp.FormatCredential rejects.
type nilPayloadMethod struct{}

func (nilPayloadMethod) Name() string   { return "tempo" }
func (nilPayloadMethod) Intent() string { return "charge" }
func (nilPayloadMethod) CreateCredential(_ context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	return challenge.NewCredential(nil), nil
}

func TestTransportReportsUnformattableCredentialLocally(t *testing.T) {
	intent, err := server.NewIntent("charge", server.IntentHooks{Verify: func(context.Context, *mpp.Credential, map[string]any) (*mpp.Receipt, error) {
		t.Fatal("no credential should reach the server")
		return nil, nil
	}})
	require.NoError(t, err)
	payment, err := server.New(rejectingMethod{intent}, "api.example.com", "test-secret-key-minimum-32-byte-secret")
	require.NoError(t, err)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		server.ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50"})(http.NotFoundHandler()).ServeHTTP(w, r)
	}))
	defer srv.Close()

	resp, err := New([]Method{nilPayloadMethod{}}).Get(context.Background(), srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	assert.ErrorContains(t, err, "credential payload must be an object")
	var unknown *PaymentOutcomeUnknownError
	assert.NotErrorAs(t, err, &unknown, "nothing was sent, so the outcome is known")
	assert.EqualValues(t, 1, requests.Load(), "the credential must not be sent")
}
