package chargeclient

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

func TestValidateOrigin(t *testing.T) {
	t.Parallel()
	method, err := New(Config{
		PrivateKey: testPrivateKey,
		TrustedProofRealms: []TrustedProofRealm{
			{Origin: "HTTP://LOCALHOST:8080/", Realm: "MPP Payment"},
			{Realm: "mcp.example"},
		},
	})
	require.NoError(t, err)
	proof := func(realm string) *mpp.Challenge {
		challenge, err := mpp.NewChallengeWithError("secret", realm, tempo.MethodName, tempo.IntentCharge, map[string]any{"amount": "0"})
		require.NoError(t, err)
		return challenge
	}
	parse := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		return u
	}

	require.NoError(t, method.ValidateOrigin(parse("https://api.example/paid"), proof("api.example")))
	require.NoError(t, method.ValidateOrigin(parse("http://localhost:8080/paid"), proof("MPP Payment")))
	require.NoError(t, method.ValidateOrigin(nil, proof("mcp.example")))
	require.ErrorContains(t, method.ValidateOrigin(parse("http://api.example/paid"), proof("api.example")), "refusing to sign Tempo proof for realm")
	require.ErrorContains(t, method.ValidateOrigin(parse("https://evil.example/paid"), proof("api.example")), "refusing to sign Tempo proof for realm")
	require.Error(t, method.ValidateOrigin(parse("http://localhost:8080/paid"), proof("mpp payment")), "realms compare exactly")
	require.Error(t, method.ValidateOrigin(parse("http://localhost:9090/paid"), proof("MPP Payment")))
	require.Error(t, method.ValidateOrigin(parse("https://mcp.example:8443"), proof("mcp.example")), "an MCP-only realm is not trusted over HTTP")
	require.ErrorContains(t, method.ValidateOrigin(nil, proof("api.example")), "untrusted realm")

	paid, err := mpp.NewChallengeWithError("secret", "elsewhere.example", tempo.MethodName, tempo.IntentCharge, map[string]any{"amount": "1"})
	require.NoError(t, err)
	require.NoError(t, method.ValidateOrigin(parse("https://api.example"), paid), "paid challenges are not proofs")
}

func TestNewBuildsRPCClientOnce(t *testing.T) {
	t.Parallel()
	method, err := New(Config{PrivateKey: testPrivateKey, RPCURL: "https://rpc.example"})
	require.NoError(t, err)
	require.NotNil(t, method.rpc)
	first, _, err := method.resolveRPC(tempo.ChargeRequest{})
	require.NoError(t, err)
	second, _, err := method.resolveRPC(tempo.ChargeRequest{})
	require.NoError(t, err)
	assert.Same(t, first, second)

	byChain, err := New(Config{PrivateKey: testPrivateKey})
	require.NoError(t, err)
	_, rpcURL, err := byChain.resolveRPC(tempo.ChargeRequest{})
	require.NoError(t, err)
	assert.Equal(t, "https://rpc.tempo.xyz", rpcURL)
	assert.Same(t, byChain.rpcClients.Get(rpcURL), byChain.rpcClients.Get(rpcURL))
}
