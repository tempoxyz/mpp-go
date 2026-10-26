package mcp_test

import (
	"context"
	"errors"
	"net/url"
	"testing"

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
	assert.Len(t, trusted.credentials(), 1, "the validator trusts the realm, so the proof is created")
	require.NotEmpty(t, trusted.origins)
	assert.Nil(t, trusted.origins[0], "MCP has no origin")

	refused := &validatingProofMethod{trusted: "other.example"}
	session = connect(t, zeroTempoChallengeServer(t, "victim.example"), mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{refused}}))
	_, err = callPremium(context.Background(), session, nil)
	require.ErrorContains(t, err, "validator refused realm")
	assert.Empty(t, refused.credentials())

}
