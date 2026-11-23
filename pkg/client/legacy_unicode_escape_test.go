package client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// legacyChallengeHeader is a Challenge as servers on older SDK releases
// wrote it, with a realm character above U+00FF escaped as \uXXXX.
func legacyChallengeHeader(id, realm string) string {
	return `Payment id="` + id + `", realm="` + realm + `", method="tempo", intent="payment", request="e30"`
}

func TestTransportRefusesLegacyUnicodeEscapedChallenge(t *testing.T) {
	for _, realm := range []string{`caf\u00e9.example.com`, `merchant\u2014api.example.com`} {
		t.Run(realm, func(t *testing.T) {
			var paidRequests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					paidRequests++
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set("WWW-Authenticate", legacyChallengeHeader("legacy", realm))
				w.WriteHeader(http.StatusPaymentRequired)
			}))
			defer srv.Close()

			method := &recordingMethod{}
			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			resp, err := newTestTransport([]Method{method}, nil).RoundTrip(req)
			assert.Nil(t, resp)
			require.ErrorIs(t, err, mpp.ErrLegacyUnicodeEscapedChallenge)
			require.ErrorIs(t, err, ErrNoMethodFound)
			assert.Contains(t, err.Error(), "upgrade the server")
			assert.Empty(t, method.ids, "no credential may be created")
			assert.Zero(t, paidRequests)
		})
	}
}

func TestTransportSkipsLegacyUnicodeEscapedChallengeForAnotherOffer(t *testing.T) {
	var valid *mpp.Challenge
	var paidWith string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			credential, err := mpp.ParseCredential(auth)
			require.NoError(t, err)
			paidWith = credential.Challenge.ID
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Add("WWW-Authenticate", legacyChallengeHeader("legacy", `merchant\u2014api.example.com`))
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
}

func TestTransportPaysChallengeWithPlainQuotedPairs(t *testing.T) {
	for _, realm := range []string{`api.\"example.com`, `api.\\example.com`} {
		t.Run(realm, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set("WWW-Authenticate", legacyChallengeHeader("plain", realm)+`, description="Payment \u2014 coffee", x-display="\u2014"`)
				w.WriteHeader(http.StatusPaymentRequired)
			}))
			defer srv.Close()

			method := &recordingMethod{}
			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			resp, err := newTestTransport([]Method{method}, nil).RoundTrip(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, []string{"plain"}, method.ids)
		})
	}
}

func TestSelectChallengeCandidatesRefusesLegacyUnicodeEscapedChallenge(t *testing.T) {
	t.Parallel()

	legacy, err := mpp.ParseChallenge(legacyChallengeHeader("legacy", `caf\u00e9.example.com`))
	require.NoError(t, err)
	plain, err := mpp.ParseChallenge(legacyChallengeHeader("plain", "api.example.com") + `, description="Payment \u2014 coffee"`)
	require.NoError(t, err)
	methods := []Method{&recordingMethod{}}

	candidates, err := SelectChallengeCandidates([]mpp.Challenge{*legacy}, methods, nil)
	require.ErrorIs(t, err, mpp.ErrLegacyUnicodeEscapedChallenge)
	assert.Empty(t, candidates)

	candidates, err = SelectChallengeCandidates([]mpp.Challenge{*legacy, *plain}, methods, nil)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.Equal(t, "plain", candidates[0].Challenge.ID)
}
