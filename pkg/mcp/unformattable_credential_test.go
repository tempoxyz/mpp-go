package mcp_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/client"
	mcpclient "github.com/tempoxyz/mpp-go/pkg/mcp/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// nilPayloadClientMethod returns a Credential mpp.FormatCredential rejects.
type nilPayloadClientMethod struct{}

func (nilPayloadClientMethod) Name() string   { return "test" }
func (nilPayloadClientMethod) Intent() string { return "charge" }
func (nilPayloadClientMethod) CreateCredential(_ context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	return challenge.NewCredential(nil), nil
}

func TestClientReportsUnformattableCredentialLocally(t *testing.T) {
	method := &testMethod{}
	c := mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{nilPayloadClientMethod{}}})
	session := connect(t, newServer(chargeHandler(newPayment(t, method))), c)

	_, err := callPremium(context.Background(), session, nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "credential payload must be an object")
	assert.Empty(t, method.recorded(), "the credential must not be sent")
}
