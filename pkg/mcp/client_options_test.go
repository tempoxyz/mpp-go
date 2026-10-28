package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/internal/mpptest"
	mcpclient "github.com/tempoxyz/mpp-go/pkg/mcp/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	chargeclient "github.com/tempoxyz/mpp-go/pkg/tempo/client"
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
	assert.Empty(t, trusted.credentials(), "origin-less realm trust alone must not authorize an MCP session")
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

// realProofMethod pays tempo/charge proofs and validates origins with the
// Tempo client method's own rule.
type realProofMethod struct {
	tempoProofMethod
	validator *chargeclient.Method
}

func (m *realProofMethod) ValidateOrigin(origin *url.URL, challenge *mpp.Challenge) error {
	return m.validator.ValidateOrigin(origin, challenge)
}

func newRealProofMethod(t *testing.T, trusted ...chargeclient.TrustedProofRealm) *realProofMethod {
	t.Helper()
	validator, err := chargeclient.New(chargeclient.Config{
		PrivateKey:         "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80",
		TrustedProofRealms: trusted,
	})
	require.NoError(t, err)
	return &realProofMethod{validator: validator}
}

// connectHTTPS connects c to srv over Streamable HTTP at endpoint, an
// https://<host>/ URL the test server's certificate (issued for example.com)
// is accepted for, whatever host it names.
func connectHTTPS(t *testing.T, srv *mcp.Server, c *mcp.Client, endpoint string) *mcp.ClientSession {
	t.Helper()
	// The test server listens on localhost but is reached under other host names.
	ts := httptest.NewTLSServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true}))
	t.Cleanup(ts.Close)
	transport := ts.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	addr := ts.Listener.Addr().String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	session, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: transport},
		MaxRetries: -1,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// E11: a shared client signs a realm's proof only for the session connected
// to that realm's endpoint, without any configuration, so a server cannot
// relay another server's challenge.
func TestProofIsImplicitlyBoundToSessionEndpoint(t *testing.T) {
	payer := newRealProofMethod(t)
	c := mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{payer}})
	// A per-call payment approval override must not bypass session binding.
	ctx := mcpclient.WithOnPaymentRequired(context.Background(), nil)

	relay := connectHTTPS(t, zeroTempoChallengeServer(t, "example.com"), c, "https://attacker.test/")
	_, err := callPremium(ctx, relay, nil)
	require.ErrorContains(t, err, "refusing to sign Tempo proof")
	assert.Empty(t, payer.credentials())

	local := connect(t, zeroTempoChallengeServer(t, "example.com"), c)
	_, err = callPremium(ctx, local, nil)
	require.ErrorContains(t, err, "Config.ValidateTempoProofSession")
	assert.Empty(t, payer.credentials())

	owner := connectHTTPS(t, zeroTempoChallengeServer(t, "example.com"), c, "https://example.com/mcp")
	_, err = callPremium(ctx, owner, nil)
	require.Error(t, err) // the mock keeps offering its challenge
	assert.Len(t, payer.credentials(), 1)

	_, err = callPremium(ctx, relay, nil)
	require.ErrorContains(t, err, "refusing to sign Tempo proof")
	assert.Len(t, payer.credentials(), 1)
}

func TestTrustedProofRealmMatchesSessionEndpointOrigin(t *testing.T) {
	// An origin-less entry no longer trusts the realm for every session.
	originless := newRealProofMethod(t, chargeclient.TrustedProofRealm{Realm: "MPP Payment"})
	session := connectHTTPS(t, zeroTempoChallengeServer(t, "MPP Payment"), mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{originless}}), "https://example.com/")
	_, err := callPremium(context.Background(), session, nil)
	require.ErrorContains(t, err, "refusing to sign Tempo proof")
	assert.Empty(t, originless.credentials())

	trusted := newRealProofMethod(t, chargeclient.TrustedProofRealm{Origin: "https://example.com", Realm: "MPP Payment"})
	session = connectHTTPS(t, zeroTempoChallengeServer(t, "MPP Payment"), mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{trusted}}), "https://example.com/")
	_, err = callPremium(context.Background(), session, nil)
	require.Error(t, err)
	assert.Len(t, trusted.credentials(), 1)
}

func TestProofWithoutOriginValidatorUsesSessionEndpoint(t *testing.T) {
	payer := &tempoProofMethod{}
	c := mcpclient.Wrap(newClient(), mcpclient.Config{Methods: []client.Method{payer}})
	session := connectHTTPS(t, zeroTempoChallengeServer(t, "example.com"), c, "https://attacker.test/")
	_, err := callPremium(context.Background(), session, nil)
	require.ErrorContains(t, err, "refusing to sign Tempo proof")
	assert.Empty(t, payer.credentials())

	session = connectHTTPS(t, zeroTempoChallengeServer(t, "example.com"), c, "https://example.com/")
	_, err = callPremium(context.Background(), session, nil)
	require.Error(t, err)
	assert.Len(t, payer.credentials(), 1)
}

// A re-offer that keeps a cached proof's ID but changes its amount must not
// resubmit the proof without the session check.
func TestChangedAmountReofferDoesNotReuseCachedProof(t *testing.T) {
	proof := mpptest.NewChallenge(secretKey, "victim.example", "tempo", "charge", map[string]any{"amount": "0", "currency": "0x20c0"}, mpp.WithExpires(mpp.ExpiresIn(5*time.Minute)))
	paid := mpptest.NewChallenge(secretKey, "victim.example", "tempo", "charge", map[string]any{"amount": "1", "currency": "0x20c0"}, mpp.WithExpires(proof.Expires))
	paid.ID = proof.ID
	var mu sync.Mutex
	var submitted []string
	srv := newServer(func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		challenge := proof
		if raw, ok := req.Params.Meta[mpp.MCPCredentialMetaKey]; ok {
			encoded, err := json.Marshal(raw)
			require.NoError(t, err)
			var credential mpp.Credential
			require.NoError(t, json.Unmarshal(encoded, &credential))
			mu.Lock()
			amount := "1"
			if credential.Challenge.Request == proof.ToEcho().Request {
				amount = "0"
			}
			submitted = append(submitted, amount)
			mu.Unlock()
			challenge = paid
		}
		data, err := json.Marshal(mpp.MCPPaymentRequiredData{HTTPStatus: 402, Challenges: []*mpp.Challenge{challenge}})
		require.NoError(t, err)
		return nil, &jsonrpc.Error{Code: mpp.MCPPaymentRequiredCode, Message: "Payment Required", Data: data}
	})
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
	session := connect(t, srv, c)
	_, err := callPremium(context.Background(), session, nil)
	require.Error(t, err)
	assert.Equal(t, 1, checks)
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, submitted)
	assert.Equal(t, "0", submitted[0])
	for _, amount := range submitted[1:] {
		assert.Equal(t, "1", amount, "the zero-amount proof was resubmitted for a changed challenge")
	}
}
