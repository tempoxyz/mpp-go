package mcp_test

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/client"
	mcpclient "github.com/tempoxyz/mpp-go/pkg/mcp/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

func TestMiddlewarePaysWithoutWrap(t *testing.T) {
	method := &testMethod{}
	payer := &testClientMethod{token: "valid"}
	c := newClient()
	c.AddSendingMiddleware(mcpclient.Middleware(mcpclient.Config{Methods: []client.Method{payer}}))
	session := connect(t, newServer(chargeHandler(newPayment(t, method))), c)

	result, err := callPremium(context.Background(), session, nil)
	require.NoError(t, err)
	receipt, err := mcpclient.Receipt(result)
	require.NoError(t, err)
	require.NotNil(t, receipt)
	assert.Len(t, payer.credentials(), 1)
}

func TestErrPaymentDeclinedIsShared(t *testing.T) {
	assert.Equal(t, client.ErrPaymentDeclined, mcpclient.ErrPaymentDeclined)
	payer := &testClientMethod{token: "valid"}
	c := newClient()
	c.AddSendingMiddleware(mcpclient.Middleware(mcpclient.Config{
		Methods:           []client.Method{payer},
		OnPaymentRequired: func(context.Context, *mpp.Challenge) (bool, error) { return false, nil },
	}))
	session := connect(t, newServer(chargeHandler(newPayment(t, &testMethod{}))), c)
	_, err := callPremium(context.Background(), session, nil)
	require.ErrorIs(t, err, client.ErrPaymentDeclined)
}

// validatingProofMethod pays tempo/charge proofs for one realm, which it
// trusts on transports without an origin.
type validatingProofMethod struct {
	tempoProofMethod
	trusted string
	origins []*url.URL
}

func (m *validatingProofMethod) ValidateOrigin(origin *url.URL, challenge *mpp.Challenge) error {
	m.origins = append(m.origins, origin)
	if origin == nil && challenge.Realm == m.trusted {
		return nil
	}
	return errors.New("validator refused realm")
}

func TestMCPClientDefersToOriginValidator(t *testing.T) {
	trusted := &validatingProofMethod{trusted: "victim.example"}
	session := connect(t, zeroTempoChallengeServer(t, "victim.example"), mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{trusted}}))
	_, err := callPremium(context.Background(), session, nil)
	require.Error(t, err)
	assert.Empty(t, trusted.credentials(), "realm trust alone must not authorize an MCP session")
	assert.Empty(t, trusted.origins)

	refused := &validatingProofMethod{trusted: "other.example"}
	session = connect(t, zeroTempoChallengeServer(t, "victim.example"), mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{refused}, ValidateTempoProofSession: func(context.Context, *mcp.ClientSession, string) error { return nil }}))
	_, err = callPremium(context.Background(), session, nil)
	require.ErrorContains(t, err, "validator refused realm")
	assert.Empty(t, refused.credentials())

}

func TestProofTrustIsScopedToSession(t *testing.T) {
	payer := &validatingProofMethod{trusted: "victim.example"}
	c := newClient()
	trusted := connect(t, zeroTempoChallengeServer(t, "victim.example"), c)
	untrusted := connect(t, zeroTempoChallengeServer(t, "victim.example"), c)
	mcpclient.Wrap(c, mcpclient.Config{
		Methods: []client.Method{payer},
		ValidateTempoProofSession: func(_ context.Context, session *mcp.ClientSession, realm string) error {
			if session != trusted || realm != "victim.example" {
				return errors.New("untrusted proof session")
			}
			return nil
		},
	})
	// A per-call payment approval override must not bypass session trust.
	ctx := mcpclient.WithOnPaymentRequired(context.Background(), nil)
	_, err := callPremium(ctx, untrusted, nil)
	require.ErrorContains(t, err, "untrusted proof session")
	assert.Empty(t, payer.credentials())
	_, err = callPremium(ctx, trusted, nil)
	require.Error(t, err) // the mock keeps offering its challenge
	assert.Len(t, payer.credentials(), 1)
	_, err = callPremium(ctx, untrusted, nil)
	require.ErrorContains(t, err, "untrusted proof session")
	assert.Len(t, payer.credentials(), 1)
}

func TestCachedProofRechecksSessionTrust(t *testing.T) {
	payer := &validatingProofMethod{trusted: "victim.example"}
	checks := 0
	c := mcpclient.Wrap(newClient(), mcpclient.Config{
		Methods: []client.Method{payer},
		ValidateTempoProofSession: func(context.Context, *mcp.ClientSession, string) error {
			checks++
			if checks > 1 {
				return errors.New("session trust revoked")
			}
			return nil
		},
	})
	session := connect(t, zeroTempoChallengeServer(t, "victim.example"), c)
	_, err := callPremium(context.Background(), session, nil)
	require.ErrorContains(t, err, "session trust revoked")
	assert.Equal(t, 2, checks)
	assert.Len(t, payer.credentials(), 1)
}
