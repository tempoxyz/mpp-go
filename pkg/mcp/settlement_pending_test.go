package mcp_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/internal/paymentctx"
	mcpclient "github.com/tempoxyz/mpp-go/pkg/mcp/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// settlingClientMethod reports its payment still settling for its first
// pending calls, then pays, recording each challenge ID and origin.
type settlingClientMethod struct {
	testClientMethod
	pending    int
	challenges []string
	origins    []string
}

func (m *settlingClientMethod) CreateCredential(ctx context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	m.challenges = append(m.challenges, challenge.ID)
	m.origins = append(m.origins, paymentctx.Origin(ctx))
	if len(m.challenges) <= m.pending {
		return nil, fmt.Errorf("still settling: %w", paymentctx.ErrSettlementPending)
	}
	return m.testClientMethod.CreateCredential(ctx, challenge)
}

// A payment still settling is resumed for the same challenge, with no
// origin, without paying again.
func TestClientRetriesSettlementPendingForSameChallenge(t *testing.T) {
	method := &testMethod{}
	payer := &settlingClientMethod{testClientMethod: testClientMethod{token: "valid"}, pending: 1}
	c := mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{payer}})
	session := connect(t, newServer(chargeHandler(newPayment(t, method))), c)

	_, err := callPremium(context.Background(), session, nil)
	require.NoError(t, err)
	require.Len(t, payer.challenges, 2)
	assert.Equal(t, payer.challenges[0], payer.challenges[1])
	assert.Equal(t, []string{"", ""}, payer.origins)
	assert.Equal(t, []string{"A"}, method.recorded())
}

// It stops at the payment-attempt limit and returns the error.
func TestClientStopsRetryingSettlementPending(t *testing.T) {
	method := &testMethod{}
	payer := &settlingClientMethod{testClientMethod: testClientMethod{token: "valid"}, pending: 100}
	c := mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{payer}})
	session := connect(t, newServer(chargeHandler(newPayment(t, method))), c)

	_, err := callPremium(context.Background(), session, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "settlement pending")
	assert.Greater(t, len(payer.challenges), 1)
	for _, id := range payer.challenges {
		assert.Equal(t, payer.challenges[0], id)
	}
	assert.Empty(t, method.recorded())
}
