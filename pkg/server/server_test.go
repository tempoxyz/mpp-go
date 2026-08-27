package server

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRejectsWeakSecret(t *testing.T) {
	t.Parallel()

	method := chargeTestMethod{intents: map[string]Intent{"charge": verifyTestIntent{}}}
	for _, tc := range []struct {
		name   string
		secret string
	}{
		{"empty", ""},
		{"thirtyOneBytes", strings.Repeat("a", 31)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(method, "api.example.com", tc.secret)
			assert.EqualError(t, err, "server: secret key must be at least 32 bytes")
		})
	}
}

func TestNewAcceptsMinimumLengthSecret(t *testing.T) {
	t.Parallel()

	method := chargeTestMethod{intents: map[string]Intent{"charge": verifyTestIntent{}}}
	payment, err := New(method, "api.example.com", strings.Repeat("a", 32))
	require.NoError(t, err)
	require.NotNil(t, payment)
}

// newTestServer constructs an Mpp for tests and fails the test if New rejects
// the secret. Callers must pass a secret of at least minimumSecretKeyBytes.
func newTestServer(t *testing.T, method Method, realm, secretKey string, opts ...Option) *Mpp {
	t.Helper()
	payment, err := New(method, realm, secretKey, opts...)
	require.NoError(t, err)
	return payment
}
