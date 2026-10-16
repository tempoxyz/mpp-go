package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

type regeneratingMethod struct{ calls int }

func (*regeneratingMethod) Name() string   { return "tempo" }
func (*regeneratingMethod) Intent() string { return "charge" }
func (m *regeneratingMethod) CreateCredential(_ context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	m.calls++
	return challenge.NewCredential(map[string]any{"proof": fmt.Sprint(m.calls)}), nil
}

type rejectingMethod struct{ intent server.Intent }

func (rejectingMethod) Name() string { return "tempo" }
func (m rejectingMethod) Intents() map[string]server.Intent {
	return map[string]server.Intent{"charge": m.intent}
}

func TestTransportRegeneratesInvalidPayloadWithFixedExpiry(t *testing.T) {
	for _, requiresAuth := range []bool{false, true} {
		for _, alwaysReject := range []bool{false, true} {
			t.Run(fmt.Sprintf("requiresAuth=%t/alwaysReject=%t", requiresAuth, alwaysReject), func(t *testing.T) {
				var ids, proofs []string
				intent, err := server.NewIntent("charge", server.IntentHooks{Verify: func(_ context.Context, cred *mpp.Credential, _ map[string]any) (*mpp.Receipt, error) {
					ids = append(ids, cred.Challenge.ID)
					proofs = append(proofs, cred.Payload["proof"].(string))
					if alwaysReject || cred.Payload["proof"] == "1" {
						return nil, mpp.ErrInvalidPayload("proof rejected")
					}
					return mpp.Success("tempo", "receipt-1"), nil
				}})
				require.NoError(t, err)
				var opts []server.Option
				if requiresAuth {
					opts = append(opts, server.WithRequiresAuth(true))
				}
				payment, err := server.New(rejectingMethod{intent}, "api.example.com", "test-secret-key-minimum-32-byte-secret", opts...)
				require.NoError(t, err)
				srv := httptest.NewServer(server.ChargeMiddleware(payment, server.ChargeParams{Amount: "0.50", Expires: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })))
				defer srv.Close()
				method := new(regeneratingMethod)
				resp, err := New([]Method{method}).Get(context.Background(), srv.URL)
				require.NoError(t, err)
				defer resp.Body.Close()
				if alwaysReject {
					assert.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
					assert.Equal(t, defaultMaxPaymentRetries, method.calls)
					assert.Equal(t, []string{"1", "2", "3"}, proofs)
					body, err := io.ReadAll(resp.Body)
					require.NoError(t, err)
					assert.Contains(t, string(body), "https://paymentauth.org/problems/invalid-payload")
				} else {
					assert.Equal(t, http.StatusOK, resp.StatusCode)
					assert.Equal(t, 2, method.calls)
					assert.Equal(t, []string{"1", "2"}, proofs)
				}
				require.NotEmpty(t, ids)
				for _, id := range ids {
					assert.Equal(t, ids[0], id)
				}
			})
		}
	}
}

func TestTransportInvalidPayloadResponseClassification(t *testing.T) {
	const invalid = `{"type":"https://paymentauth.org/problems/invalid-payload"}`
	for _, tc := range []struct {
		name, contentType, body string
		calls                   int
	}{
		{"invalid payload", "application/problem+json", invalid, 2},
		{"content type parameters", "application/problem+json; charset=utf-8", invalid, 2},
		{"other problem", "application/problem+json", `{"type":"https://paymentauth.org/problems/verification-failed"}`, 1},
		{"missing type", "application/problem+json", `{}`, 1},
		{"malformed", "application/problem+json", `{"type":`, 1},
		{"wrong content type", "text/plain", invalid, 1},
		{"missing content type", "", invalid, 1},
		{"oversized", "application/problem+json", strings.Repeat(" ", 64<<10) + invalid, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			challenge := mpp.NewChallenge("secret", "example.com", "tempo", "payment", nil)
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				if requests == 3 {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set(mpp.HeaderWWWAuthenticate, challenge.ToAuthenticate(challenge.Realm))
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(http.StatusPaymentRequired)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			method := &mockMethod{name: "tempo", cred: newTestCredential("tempo")}
			resp, err := New([]Method{method}).Get(context.Background(), srv.URL)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, tc.calls, method.calls)
		})
	}
}
