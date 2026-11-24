package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/internal/paymentctx"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// settlingMethod reports its payment still settling for its first pending
// calls, then pays, recording each challenge ID and origin it was asked for.
type settlingMethod struct {
	pending    int
	mu         sync.Mutex
	challenges []string
	origins    []string
}

func (*settlingMethod) Name() string   { return "tempo" }
func (*settlingMethod) Intent() string { return "charge" }

func (m *settlingMethod) CreateCredential(ctx context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.challenges = append(m.challenges, challenge.ID)
	m.origins = append(m.origins, paymentctx.Origin(ctx))
	if len(m.challenges) <= m.pending {
		return nil, fmt.Errorf("still settling: %w", paymentctx.ErrSettlementPending)
	}
	return challenge.NewCredential(map[string]any{"proof": "ok"}), nil
}

// newSettlingServer answers an unpaid request with one challenge and a paid
// one with 200, counting the challenges it issued.
func newSettlingServer(t *testing.T, issued *int) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Authorization"), "Payment ") {
			w.WriteHeader(http.StatusOK)
			return
		}
		mu.Lock()
		*issued++
		mu.Unlock()
		challenge := mpptest.NewChallenge("secret", r.Host, "tempo", "charge", map[string]any{"amount": "1"}, mpp.WithExpires(mpp.ExpiresIn(time.Minute)))
		w.Header().Set("WWW-Authenticate", mpptest.FormatChallenge(challenge))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A payment still settling is resumed for the same challenge, with the
// issuing server's origin, without requesting a new challenge.
func TestTransportRetriesSettlementPendingForSameChallenge(t *testing.T) {
	var issued int
	srv := newSettlingServer(t, &issued)
	method := &settlingMethod{pending: 2}
	resp, err := New([]Method{method}).Get(context.Background(), srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, issued)
	require.Len(t, method.challenges, 3)
	assert.Equal(t, method.challenges[0], method.challenges[1])
	assert.Equal(t, method.challenges[0], method.challenges[2])
	assert.Equal(t, []string{srv.URL, srv.URL, srv.URL}, method.origins)
}

// It stops at the payment-retry limit.
func TestTransportStopsRetryingSettlementPendingAtLimit(t *testing.T) {
	var issued int
	srv := newSettlingServer(t, &issued)
	method := &settlingMethod{pending: 100}
	_, err := New([]Method{method}, WithMaxRetries(2)).Get(context.Background(), srv.URL)
	require.ErrorIs(t, err, paymentctx.ErrSettlementPending)
	assert.Len(t, method.challenges, 2)
	assert.Equal(t, 1, issued)
}

// A request whose URL names no host passes no origin, and still retries.
func TestTransportRetriesSettlementPendingWithoutOrigin(t *testing.T) {
	challenge := mpptest.NewChallenge("secret", "api.example.com", "tempo", "charge", map[string]any{"amount": "1"}, mpp.WithExpires(mpp.ExpiresIn(time.Minute)))
	inner := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.Header.Get("Authorization"), "Payment ") {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: r}, nil
		}
		return &http.Response{StatusCode: http.StatusPaymentRequired, Body: io.NopCloser(strings.NewReader("")),
			Header: http.Header{"Www-Authenticate": {mpptest.FormatChallenge(challenge)}}}, nil
	})
	method := &settlingMethod{pending: 1}
	request, err := http.NewRequest(http.MethodGet, "/paid", nil)
	require.NoError(t, err)
	resp, err := newTestTransport([]Method{method}, inner).RoundTrip(request)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{challenge.ID, challenge.ID}, method.challenges)
	assert.Equal(t, []string{"", ""}, method.origins)
	assert.Empty(t, challengeOrigin(request.URL))
}
