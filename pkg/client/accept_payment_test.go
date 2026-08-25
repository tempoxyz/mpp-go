package client

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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
		{
			name:       "q-value above one",
			configured: PaymentPreferences{"tempo/charge": 1.1},
			wantErr:    "between 0 and 1",
		},
		{
			name:       "non-finite q-value",
			configured: PaymentPreferences{"tempo/charge": math.Inf(1)},
			wantErr:    "between 0 and 1",
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

func TestParseAcceptPayment(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		want       []paymentPreference
		normalized string
		wantErr    bool
	}{
		{
			name:       "single capability",
			header:     "tempo/charge",
			want:       []paymentPreference{{method: "tempo", intent: "charge", quality: 1, specificity: 2}},
			normalized: "tempo/charge",
		},
		{
			name:   "whitespace wildcards and q-values",
			header: " stripe/charge ; q = 0.25 , */session ; q=0 ",
			want: []paymentPreference{
				{method: "stripe", intent: "charge", quality: 0.25, specificity: 2},
				{method: "*", intent: "session", quality: 0, index: 1, specificity: 1},
			},
			normalized: "stripe/charge;q=0.25, */session;q=0",
		},
		{
			name:   "specific opt out",
			header: "tempo/*;q=1, tempo/charge;q=0, stripe/*;q=0.5",
			want: []paymentPreference{
				{method: "tempo", intent: "*", quality: 1, specificity: 1},
				{method: "tempo", intent: "charge", quality: 0, index: 1, specificity: 2},
				{method: "stripe", intent: "*", quality: 0.5, index: 2, specificity: 1},
			},
			normalized: "tempo/*, tempo/charge;q=0, stripe/*;q=0.5",
		},
		{
			name:   "quoted extensions and case-insensitive q",
			header: `tempo/charge;profile="a,b;c";Q=0, stripe/charge`,
			want: []paymentPreference{
				{method: "tempo", intent: "charge", quality: 0, specificity: 2},
				{method: "stripe", intent: "charge", quality: 1, index: 1, specificity: 2},
			},
			normalized: "tempo/charge;q=0, stripe/charge",
		},
		{
			name:       "custom HTTP tokens",
			header:     "Custom/pre_authorize",
			want:       []paymentPreference{{method: "Custom", intent: "pre_authorize", quality: 1, specificity: 2}},
			normalized: "Custom/pre_authorize",
		},
		{name: "empty", header: "", wantErr: true},
		{name: "missing intent", header: "tempo", wantErr: true},
		{name: "extra slash", header: "tempo/charge/extra", wantErr: true},
		{name: "q-value above one", header: "tempo/charge;q=2", wantErr: true},
		{name: "q-value too precise", header: "tempo/charge;q=0.1234", wantErr: true},
		{name: "missing parameter value", header: "tempo/charge;q", wantErr: true},
		{name: "invalid parameter name", header: "tempo/charge;bad name=value", wantErr: true},
		{name: "unterminated quote", header: `tempo/charge;profile="unterminated`, wantErr: true},
		{name: "trailing comma", header: "tempo/charge,", wantErr: true},
		{name: "conflicting duplicate q", header: "tempo/charge;q=0;q=1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAcceptPayment(tt.header)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.normalized, serializeAcceptPayment(got))
		})
	}
}

func TestSplitHeaderList(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		want    []string
		wantErr bool
	}{
		{
			name:   "simple list",
			header: "tempo/charge, stripe/charge",
			want:   []string{"tempo/charge", "stripe/charge"},
		},
		{
			name:   "quoted comma and semicolon",
			header: `tempo/charge;profile="a,b;c", stripe/charge`,
			want:   []string{`tempo/charge;profile="a,b;c"`, "stripe/charge"},
		},
		{
			name:   "escaped quote before comma",
			header: `tempo/charge;profile="a\",b", stripe/charge`,
			want:   []string{`tempo/charge;profile="a\",b"`, "stripe/charge"},
		},
		{name: "unterminated quote", header: `tempo/charge;profile="a,b`, wantErr: true},
		{name: "dangling quoted escape", header: `tempo/charge;profile="a\`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splitHeaderList(tt.header)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBestPaymentPreference(t *testing.T) {
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
			preference, ok := bestPaymentPreference(&mpp.Challenge{Method: tt.method, Intent: tt.intent}, preferences)
			assert.Equal(t, tt.wantMatch, ok)
			if ok {
				assert.Equal(t, tt.want, preference.quality)
			}
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

func TestTransportChallengeCandidates(t *testing.T) {
	tempoCharge := &mockMethod{name: "tempo", intent: "charge"}
	tempoSession := &mockMethod{name: "tempo", intent: "session"}
	stripeCharge := &mockMethod{name: "stripe", intent: "charge"}
	primarySession := &matchingMockMethod{
		mockMethod: &mockMethod{name: "tempo", intent: "session"},
		canHandle: func(challenge *mpp.Challenge) bool {
			details, _ := challenge.Request["methodDetails"].(map[string]any)
			return details["sessionProtocol"] == "primary"
		},
	}
	fallbackSession := &matchingMockMethod{
		mockMethod: &mockMethod{name: "tempo", intent: "session"},
		canHandle: func(challenge *mpp.Challenge) bool {
			details, _ := challenge.Request["methodDetails"].(map[string]any)
			protocol, _ := details["sessionProtocol"].(string)
			return protocol == "" || protocol == "fallback"
		},
	}
	now := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		methods     []Method
		challenges  []mpp.Challenge
		preferences string
		wantIDs     []string
		wantMethods []Method
	}{
		{
			name:    "higher q then server order for ties",
			methods: []Method{tempoCharge, stripeCharge, tempoSession},
			challenges: []mpp.Challenge{
				{ID: "stripe", Method: "stripe", Intent: "charge"},
				{ID: "tempo-charge", Method: "tempo", Intent: "charge"},
				{ID: "tempo-session", Method: "tempo", Intent: "session"},
			},
			preferences: "stripe/charge;q=0.5, tempo/charge;q=0.9, tempo/session;q=0.9",
			wantIDs:     []string{"tempo-charge", "tempo-session", "stripe"},
		},
		{
			name:    "specific opt out overrides wildcard",
			methods: []Method{tempoCharge, stripeCharge},
			challenges: []mpp.Challenge{
				{ID: "tempo", Method: "tempo", Intent: "charge"},
				{ID: "stripe", Method: "stripe", Intent: "charge"},
			},
			preferences: "tempo/*;q=1, tempo/charge;q=0, stripe/*;q=0.5",
			wantIDs:     []string{"stripe"},
		},
		{
			name:    "unsupported and disabled offers excluded",
			methods: []Method{tempoCharge},
			challenges: []mpp.Challenge{
				{ID: "unknown", Method: "unknown", Intent: "charge"},
				{ID: "tempo", Method: "tempo", Intent: "charge"},
			},
			preferences: "tempo/charge;q=0, unknown/charge",
			wantIDs:     []string{},
		},
		{
			name:    "duplicate capabilities use predicates",
			methods: []Method{primarySession, fallbackSession},
			challenges: []mpp.Challenge{
				{ID: "primary", Method: "tempo", Intent: "session", Request: map[string]any{"methodDetails": map[string]any{"sessionProtocol": "primary"}}},
				{ID: "fallback", Method: "tempo", Intent: "session", Request: map[string]any{"methodDetails": map[string]any{"sessionProtocol": "fallback"}}},
				{ID: "unmarked", Method: "tempo", Intent: "session", Request: map[string]any{}},
			},
			preferences: "tempo/session",
			wantIDs:     []string{"primary", "fallback", "unmarked"},
			wantMethods: []Method{primarySession, fallbackSession, fallbackSession},
		},
		{
			name:    "expired and malformed expiry skipped",
			methods: []Method{tempoCharge},
			challenges: []mpp.Challenge{
				{ID: "expired", Method: "tempo", Intent: "charge", Expires: now.Add(-time.Minute).Format(time.RFC3339)},
				{ID: "malformed", Method: "tempo", Intent: "charge", Expires: "later"},
				{ID: "valid", Method: "tempo", Intent: "charge", Expires: now.Add(time.Minute).Format(time.RFC3339)},
			},
			preferences: "tempo/charge",
			wantIDs:     []string{"valid"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preferences, err := parseAcceptPayment(tt.preferences)
			require.NoError(t, err)
			transport := NewTransport(tt.methods, roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, nil
			}))

			got := transport.challengeCandidates(tt.challenges, preferences, now)
			ids := make([]string, len(got))
			for i, candidate := range got {
				ids[i] = candidate.challenge.ID
				if tt.wantMethods != nil {
					assert.Same(t, tt.wantMethods[i], candidate.method)
				}
			}
			assert.Equal(t, tt.wantIDs, ids)
		})
	}
}

type challengeSpec struct {
	method  string
	intent  string
	request map[string]any
}

func TestTransport_RoundTrip_PaymentNegotiation(t *testing.T) {
	tests := []struct {
		name       string
		setup      func() ([]Method, []*mockMethod)
		challenges []challengeSpec
		configured PaymentPreferences
		explicit   string
		wantHeader string
		wantStatus int
		wantCalls  []int
	}{
		{
			name: "challenge predicates select matching duplicate handler",
			setup: func() ([]Method, []*mockMethod) {
				primary := &mockMethod{name: "tempo", intent: "session", cred: newTestCredential("tempo")}
				fallback := &mockMethod{name: "tempo", intent: "session", cred: newTestCredential("tempo")}
				return []Method{
					&matchingMockMethod{mockMethod: primary, canHandle: func(challenge *mpp.Challenge) bool {
						return challenge.Request["protocol"] == "primary"
					}},
					&matchingMockMethod{mockMethod: fallback, canHandle: func(challenge *mpp.Challenge) bool {
						return challenge.Request["protocol"] == "fallback"
					}},
				}, []*mockMethod{primary, fallback}
			},
			challenges: []challengeSpec{{method: "tempo", intent: "session", request: map[string]any{"protocol": "fallback"}}},
			wantHeader: "tempo/session",
			wantStatus: http.StatusOK,
			wantCalls:  []int{0, 1},
		},
		{
			name: "explicit specific opt out overrides wildcard",
			setup: func() ([]Method, []*mockMethod) {
				charge := &mockMethod{name: "tempo", intent: "charge", cred: newTestCredential("tempo")}
				session := &mockMethod{name: "tempo", intent: "session", cred: newTestCredential("tempo")}
				return []Method{charge, session}, []*mockMethod{charge, session}
			},
			challenges: []challengeSpec{
				{method: "tempo", intent: "charge"},
				{method: "tempo", intent: "session"},
			},
			explicit:   "tempo/*, tempo/charge;q=0",
			wantHeader: "tempo/*, tempo/charge;q=0",
			wantStatus: http.StatusOK,
			wantCalls:  []int{0, 1},
		},
		{
			name: "malformed explicit header falls back to configured ranking",
			setup: func() ([]Method, []*mockMethod) {
				tempo := &mockMethod{name: "tempo", intent: "charge", cred: newTestCredential("tempo")}
				stripe := &mockMethod{name: "stripe", intent: "charge", cred: newTestCredential("stripe")}
				return []Method{tempo, stripe}, []*mockMethod{tempo, stripe}
			},
			challenges: []challengeSpec{
				{method: "stripe", intent: "charge"},
				{method: "tempo", intent: "charge"},
			},
			configured: PaymentPreferences{"stripe/charge": 0.5},
			explicit:   "not a valid header",
			wantHeader: "not a valid header",
			wantStatus: http.StatusOK,
			wantCalls:  []int{1, 0},
		},
		{
			name: "q zero disables every supported offer",
			setup: func() ([]Method, []*mockMethod) {
				tempo := &mockMethod{name: "tempo", intent: "charge", cred: newTestCredential("tempo")}
				return []Method{tempo}, []*mockMethod{tempo}
			},
			challenges: []challengeSpec{{method: "tempo", intent: "charge"}},
			explicit:   "tempo/charge;q=0",
			wantHeader: "tempo/charge;q=0",
			wantStatus: http.StatusPaymentRequired,
			wantCalls:  []int{0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			for _, spec := range tt.challenges {
				challenges = append(challenges, mpp.NewChallenge("secret", parsedURL.Host, spec.method, spec.intent, spec.request))
			}
			methods, tracked := tt.setup()
			transport := NewTransport(methods, nil, WithTransportPaymentPreferences(tt.configured))
			req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
			require.NoError(t, err)
			if tt.explicit != "" {
				req.Header.Set("Accept-Payment", tt.explicit)
			}

			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			assert.Equal(t, tt.wantHeader, acceptPayment)
			calls := make([]int, len(tracked))
			for i, method := range tracked {
				calls[i] = method.calls
			}
			assert.Equal(t, tt.wantCalls, calls)
		})
	}
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

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestTransportPrepareRequest(t *testing.T) {
	tempo := &mockMethod{name: "tempo", intent: "charge"}
	stripe := &mockMethod{name: "stripe", intent: "charge"}

	tests := []struct {
		name          string
		methods       []Method
		configured    PaymentPreferences
		explicit      string
		hasExplicit   bool
		nilHeader     bool
		wantHeader    string
		wantQuality   float64
		wantMatch     bool
		wantSameInput bool
	}{
		{
			name:        "advertises configured capabilities",
			methods:     []Method{tempo, stripe},
			configured:  PaymentPreferences{"tempo/charge": 0.5},
			wantHeader:  "tempo/charge;q=0.5, stripe/charge",
			wantQuality: 0.5,
			wantMatch:   true,
		},
		{
			name:          "explicit header overrides configured preferences",
			methods:       []Method{tempo, stripe},
			configured:    PaymentPreferences{"tempo/charge": 0.5},
			explicit:      "stripe/charge, tempo/charge;q=0.1",
			hasExplicit:   true,
			wantHeader:    "stripe/charge, tempo/charge;q=0.1",
			wantQuality:   0.1,
			wantMatch:     true,
			wantSameInput: true,
		},
		{
			name:          "malformed explicit header falls back without replacement",
			methods:       []Method{tempo},
			configured:    PaymentPreferences{"tempo/charge": 0.25},
			explicit:      "not a valid header",
			hasExplicit:   true,
			wantHeader:    "not a valid header",
			wantQuality:   0.25,
			wantMatch:     true,
			wantSameInput: true,
		},
		{
			name:          "no capabilities leaves request unchanged",
			wantSameInput: true,
		},
		{
			name:        "nil header is initialized on clone",
			methods:     []Method{tempo},
			nilHeader:   true,
			wantHeader:  "tempo/charge",
			wantQuality: 1,
			wantMatch:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := NewTransport(
				tt.methods,
				roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil }),
				WithTransportPaymentPreferences(tt.configured),
			)
			req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
			require.NoError(t, err)
			if tt.nilHeader {
				req.Header = nil
			}
			if tt.hasExplicit {
				req.Header.Set("Accept-Payment", tt.explicit)
			}

			got, preferences := transport.prepareRequest(req)
			assert.Equal(t, tt.wantSameInput, got == req)
			assert.Equal(t, tt.wantHeader, got.Header.Get("Accept-Payment"))
			if !tt.hasExplicit {
				assert.Empty(t, req.Header.Get("Accept-Payment"))
			}
			preference, ok := bestPaymentPreference(&mpp.Challenge{Method: "tempo", Intent: "charge"}, preferences)
			assert.Equal(t, tt.wantMatch, ok)
			if ok {
				assert.Equal(t, tt.wantQuality, preference.quality)
			}
		})
	}
}

func TestTransport_RoundTrip_InvalidPreferencesFailBeforeRequest(t *testing.T) {
	tests := []struct {
		name        string
		method      Method
		preferences PaymentPreferences
		wantErr     string
	}{
		{
			name:        "unknown capability",
			method:      &mockMethod{name: "tempo", intent: "charge"},
			preferences: PaymentPreferences{"unknown/charge": 1},
			wantErr:     `unknown payment preference "unknown/charge"`,
		},
		{
			name:        "invalid q-value",
			method:      &mockMethod{name: "tempo", intent: "charge"},
			preferences: PaymentPreferences{"tempo/charge": 0.1234},
			wantErr:     "at most three decimal places",
		},
		{
			name:    "invalid capability token",
			method:  &mockMethod{name: "bad/name", intent: "charge"},
			wantErr: `invalid payment method capability "bad/name/charge"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			inner := roundTripperFunc(func(*http.Request) (*http.Response, error) {
				called = true
				return &http.Response{Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			transport := NewTransport(
				[]Method{tt.method},
				inner,
				WithTransportPaymentPreferences(tt.preferences),
			)
			req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
			require.NoError(t, err)

			_, err = transport.RoundTrip(req)
			assert.ErrorContains(t, err, tt.wantErr)
			assert.False(t, called)
		})
	}
}
