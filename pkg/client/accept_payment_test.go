package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

func TestResolvePaymentPreferences(t *testing.T) {
	methods := []Method{
		&mockMethod{name: "tempo", intent: "charge"},
		&mockMethod{name: "tempo", intent: "charge"},
		&mockMethod{name: "tempo", intent: "session"},
		&mockMethod{name: "stripe", intent: "charge"},
		&mockMethod{name: "custom", intent: "pre_authorize"},
	}

	tests := []struct {
		name        string
		configured  PaymentPreferences
		wantHeader  string
		wantQuality []float64
		wantErr     string
	}{
		{
			name:        "defaults and deduplicates",
			wantHeader:  "tempo/charge, tempo/session, stripe/charge, custom/pre_authorize",
			wantQuality: []float64{1, 1, 1, 1},
		},
		{
			name: "configured q-values",
			configured: PaymentPreferences{
				"tempo/charge":  0,
				"stripe/charge": 0.125,
			},
			wantHeader:  "tempo/charge;q=0, tempo/session, stripe/charge;q=0.125, custom/pre_authorize",
			wantQuality: []float64{0, 1, 0.125, 1},
		},
		{
			name:       "unknown method",
			configured: PaymentPreferences{"evm/charge": 1},
			wantErr:    `unknown payment preference "evm/charge"`,
		},
		{
			name:       "invalid q-value",
			configured: PaymentPreferences{"tempo/charge": 0.1234},
			wantErr:    "at most three decimal places",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preferences, header, err := resolvePaymentPreferences(methods, tt.configured)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantHeader, header)
			qualities := make([]float64, len(preferences))
			for i, preference := range preferences {
				qualities[i] = preference.quality
			}
			assert.Equal(t, tt.wantQuality, qualities)
		})
	}
}

func TestParseAcceptPayment_BestMatch(t *testing.T) {
	preferences, err := parseAcceptPayment("tempo/*, tempo/charge;q=0, */charge;q=0.5")
	require.NoError(t, err)

	tests := []struct {
		name      string
		method    string
		intent    string
		want      float64
		wantMatch bool
	}{
		{name: "specific opt out", method: "tempo", intent: "charge", want: 0, wantMatch: true},
		{name: "method wildcard", method: "tempo", intent: "session", want: 1, wantMatch: true},
		{name: "intent wildcard", method: "stripe", intent: "charge", want: 0.5, wantMatch: true},
		{name: "unsupported", method: "stripe", intent: "session", wantMatch: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preference, ok := bestPaymentPreference(&mpp.Challenge{
				Method: tt.method,
				Intent: tt.intent,
			}, preferences)
			assert.Equal(t, tt.wantMatch, ok)
			if ok {
				assert.Equal(t, tt.want, preference.quality)
			}
		})
	}
}

func TestParseAcceptPayment_QuotedParametersAndCaseInsensitiveQuality(t *testing.T) {
	preferences, err := parseAcceptPayment(`tempo/charge;profile="a,b;c";Q=0, stripe/charge`)
	require.NoError(t, err)
	require.Len(t, preferences, 2)
	assert.Equal(t, float64(0), preferences[0].quality)
	assert.Equal(t, "stripe", preferences[1].method)
}

func TestParseAcceptPayment_RejectsMalformedValues(t *testing.T) {
	for _, value := range []string{
		"",
		"tempo",
		"tempo/charge/extra",
		"tempo/charge;q=2",
		"tempo/charge;q=0.1234",
		"tempo/charge;q",
		"tempo/charge;bad name=value",
		`tempo/charge;profile="unterminated`,
		"tempo/charge,",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := parseAcceptPayment(value)
			assert.Error(t, err)
		})
	}
}

type matchingMockMethod struct {
	*mockMethod
	canHandle func(*mpp.Challenge) bool
}

func (m *matchingMockMethod) CanHandleChallenge(challenge *mpp.Challenge) bool {
	return m.canHandle(challenge)
}

func TestTransport_RoundTrip_UsesChallengeMatcher(t *testing.T) {
	var challenge *mpp.Challenge
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	parsedURL, err := url.Parse(srv.URL)
	require.NoError(t, err)
	challenge = mpp.NewChallenge("secret", parsedURL.Host, "tempo", "session", map[string]any{
		"protocol": "fallback",
	})
	primary := &matchingMockMethod{
		mockMethod: &mockMethod{name: "tempo", intent: "session", cred: newTestCredential("tempo")},
		canHandle: func(challenge *mpp.Challenge) bool {
			return challenge.Request["protocol"] == "primary"
		},
	}
	fallback := &matchingMockMethod{
		mockMethod: &mockMethod{name: "tempo", intent: "session", cred: newTestCredential("tempo")},
		canHandle: func(challenge *mpp.Challenge) bool {
			return challenge.Request["protocol"] == "fallback"
		},
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := NewTransport([]Method{primary, fallback}, nil).RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Zero(t, primary.calls)
	assert.Equal(t, 1, fallback.calls)
}

func TestClient_PaymentPreferences(t *testing.T) {
	var challenges []*mpp.Challenge
	var acceptPayment string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			acceptPayment = r.Header.Get("Accept-Payment")
			for _, challenge := range challenges {
				w.Header().Add("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
			}
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	parsedURL, err := url.Parse(srv.URL)
	require.NoError(t, err)
	challenges = []*mpp.Challenge{
		mpp.NewChallenge("secret", parsedURL.Host, "tempo", "charge", nil),
		mpp.NewChallenge("secret", parsedURL.Host, "stripe", "charge", nil),
	}
	tempoMethod := &mockMethod{name: "tempo", intent: "charge", cred: newTestCredential("tempo")}
	stripeMethod := &mockMethod{name: "stripe", intent: "charge", cred: newTestCredential("stripe")}
	c := New(
		[]Method{tempoMethod, stripeMethod},
		WithPaymentPreferences(PaymentPreferences{"tempo/charge": 0.5}),
	)

	resp, err := c.Get(context.Background(), srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "tempo/charge;q=0.5, stripe/charge", acceptPayment)
	assert.Zero(t, tempoMethod.calls)
	assert.Equal(t, 1, stripeMethod.calls)
}

func TestTransport_RoundTrip_ExplicitAcceptPaymentOverridesConfiguredPreferences(t *testing.T) {
	var challenges []*mpp.Challenge
	var acceptPayment string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			acceptPayment = r.Header.Get("Accept-Payment")
			for _, challenge := range challenges {
				w.Header().Add("WWW-Authenticate", challenge.ToAuthenticate(challenge.Realm))
			}
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	parsedURL, err := url.Parse(srv.URL)
	require.NoError(t, err)
	challenges = []*mpp.Challenge{
		mpp.NewChallenge("secret", parsedURL.Host, "tempo", "charge", nil),
		mpp.NewChallenge("secret", parsedURL.Host, "tempo", "session", nil),
	}
	charge := &mockMethod{name: "tempo", intent: "charge", cred: newTestCredential("tempo")}
	session := &mockMethod{name: "tempo", intent: "session", cred: newTestCredential("tempo")}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Accept-Payment", "tempo/*, tempo/charge;q=0")

	resp, err := NewTransport([]Method{charge, session}, nil).RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "tempo/*, tempo/charge;q=0", acceptPayment)
	assert.Zero(t, charge.calls)
	assert.Equal(t, 1, session.calls)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestTransport_RoundTrip_AdvertisesCapabilitiesWithoutMutatingRequest(t *testing.T) {
	var acceptPayment string
	inner := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		acceptPayment = req.Header.Get("Accept-Payment")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})
	method := &mockMethod{name: "tempo", intent: "charge"}
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)

	resp, err := NewTransport([]Method{method}, inner).RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "tempo/charge", acceptPayment)
	assert.Empty(t, req.Header.Get("Accept-Payment"))
}

func TestTransport_RoundTrip_InvalidPreferencesFailBeforeRequest(t *testing.T) {
	called := false
	inner := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return &http.Response{Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	method := &mockMethod{name: "tempo", intent: "charge"}
	transport := NewTransport(
		[]Method{method},
		inner,
		WithTransportPaymentPreferences(PaymentPreferences{"unknown/charge": 1}),
	)
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)

	_, err = transport.RoundTrip(req)
	assert.ErrorContains(t, err, `unknown payment preference "unknown/charge"`)
	assert.False(t, called)
}
