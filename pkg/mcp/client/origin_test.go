package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionOriginReadsStreamableEndpoint pins the MCP Go SDK layout that
// sessionOrigin reads, so an SDK upgrade that changes it fails here rather
// than silently refusing every MCP proof.
func TestSessionOriginReadsStreamableEndpoint(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "s", Version: "1.0.0"}, nil)
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1.0.0"}, nil)

	session, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: HTTPClient(nil)}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	origin := sessionOrigin(session)
	require.NotNil(t, origin)
	assert.Equal(t, ts.URL+"/mcp", origin.String())

	// The default client follows cross-origin redirects.
	following, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = following.Close() })
	assert.Nil(t, sessionOrigin(following))

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })
	inMemory, err := c.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inMemory.Close() })
	assert.Nil(t, sessionOrigin(inMemory))
	assert.Nil(t, sessionOrigin(nil))
}
