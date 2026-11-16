package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// recordingMethod records the IDs of the challenges it creates credentials for.
type recordingMethod struct{ ids []string }

func (*recordingMethod) Name() string   { return "tempo" }
func (*recordingMethod) Intent() string { return "payment" }
func (m *recordingMethod) CreateCredential(_ context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	m.ids = append(m.ids, challenge.ID)
	return challenge.NewCredential(map[string]any{}), nil
}

func TestTransportSkipsChallengeWithUnsupportedCredentialHeader(t *testing.T) {
	for _, header := range []string{"Authorization", "X-Payment", ""} {
		t.Run("header="+header, func(t *testing.T) {
			var valid *mpp.Challenge
			invalid := `Payment id="invalid-header", realm="example.test", method="tempo", intent="payment", request="e30", header="` + header + `"`
			var paidWith string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if auth := r.Header.Get("Authorization"); auth != "" {
					credential, err := mpp.ParseCredential(auth)
					require.NoError(t, err)
					paidWith = credential.Challenge.ID
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Add("WWW-Authenticate", invalid)
				w.Header().Add("WWW-Authenticate", mpptest.FormatChallenge(valid))
				w.WriteHeader(http.StatusPaymentRequired)
			}))
			defer srv.Close()
			valid = challengeForURL(t, srv.URL, "tempo", nil)

			method := &recordingMethod{}
			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			resp, err := newTestTransport([]Method{method}, nil).RoundTrip(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, []string{valid.ID}, method.ids)
			assert.Equal(t, valid.ID, paidWith)
		})
	}
}

func TestTransportPaysNoChallengeWithOnlyUnsupportedCredentialHeader(t *testing.T) {
	for _, header := range []string{"Authorization", "X-Payment"} {
		t.Run("header="+header, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Empty(t, r.Header.Get("Authorization"))
				assert.Empty(t, r.Header.Get(header))
				w.Header().Set("WWW-Authenticate", `Payment id="invalid-header", realm="example.test", method="tempo", intent="payment", request="e30", header="`+header+`"`)
				w.WriteHeader(http.StatusPaymentRequired)
			}))
			defer srv.Close()

			method := &recordingMethod{}
			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			resp, err := newTestTransport([]Method{method}, nil).RoundTrip(req)
			assert.Nil(t, resp)
			require.ErrorIs(t, err, ErrNoMethodFound)
			assert.Empty(t, method.ids)
		})
	}
}

// A caller-set push credential whose echo carries a header a received
// challenge may not select is still recognized, so a fresh 402 is returned
// rather than paid a second time.
func TestTransportDoesNotRepayAfterCallerPushCredentialWithUnsupportedHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challengeForURL(t, "http://"+r.Host, "tempo", nil)))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()

	raw, err := json.Marshal(map[string]any{
		"challenge": map[string]any{
			"id": "earlier", "realm": "example.test", "method": "tempo", "intent": "payment",
			"request": "e30", "header": "Authorization",
		},
		"payload": map[string]any{"type": "hash", "hash": "0x01"},
	})
	require.NoError(t, err)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Payment "+base64.RawURLEncoding.EncodeToString(raw))

	method := &recordingMethod{}
	resp, err := newTestTransport([]Method{method}, nil).RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
	assert.Empty(t, method.ids)
}
