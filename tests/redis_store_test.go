//go:build integration

package tests

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	mppclient "github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	chargeserver "github.com/tempoxyz/mpp-go/pkg/tempo/server"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// redisImage is the Redis server the replay-protection test runs against.
const redisImage = "redis:7.4-alpine"

// TestIntegrationRedisStoreReplayAcrossReplicas pays a hash credential on one
// server replica and replays it on another that shares the same Redis: the
// replay must be rejected.
func TestIntegrationRedisStoreReplayAcrossReplicas(t *testing.T) {
	ctx := context.Background()
	rdb := startRedis(t)
	rpcURL := integrationRPCURL(t)
	chainID := waitForRPC(t, ctx, tempo.NewRPCClient(rpcURL))

	newReplica := func() *httptest.Server {
		intent, err := chargeserver.NewIntent(chargeserver.IntentConfig{
			Store:  tempo.NewRedisStore(rdb, time.Hour),
			RPCURL: rpcURL,
		})
		require.NoError(t, err)
		method := chargeserver.NewMethod(chargeserver.MethodConfig{
			Intent:         intent,
			Currency:       integrationCurrency,
			Recipient:      integrationRecipient,
			ChainID:        int64(chainID),
			SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
		})
		payment := newTestServer(t, method, integrationRealm, integrationSecretKey)
		srv := httptest.NewServer(paidHandler(t, payment, false))
		t.Cleanup(srv.Close)
		return srv
	}
	first, second := newReplica(), newReplica()

	transport := &captureTransport{inner: http.DefaultTransport}
	client := mppclient.New(
		[]mppclient.Method{newSharedPayerMethod(t, tempo.CredentialTypeHash)},
		mppclient.WithHTTPClient(&http.Client{Transport: transport}),
	)
	response, err := client.Get(ctx, first.URL)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	authorization := transport.Authorization()
	require.NotEmpty(t, authorization)

	keys, err := rdb.DBSize(ctx).Result()
	require.NoError(t, err)
	require.Positive(t, keys, "the used hash must be recorded in Redis")

	for _, replica := range []*httptest.Server{first, second} {
		replay, err := http.NewRequestWithContext(ctx, http.MethodGet, replica.URL, nil)
		require.NoError(t, err)
		replay.Header.Set("Authorization", authorization)
		replayResponse, err := http.DefaultClient.Do(replay)
		require.NoError(t, err)
		body, _ := io.ReadAll(replayResponse.Body)
		replayResponse.Body.Close()
		require.Equal(t, http.StatusForbidden, replayResponse.StatusCode, "replay body: %s", body)
		require.Contains(t, string(body), "already used")
	}
}

// startRedis runs a Redis container for the test and returns a client for it.
func startRedis(t *testing.T) *redis.Client {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        redisImage,
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(time.Minute),
		},
		Started: true,
	})
	testcontainers.CleanupContainer(t, container)
	require.NoError(t, err)
	address, err := container.PortEndpoint(ctx, "6379/tcp", "")
	require.NoError(t, err)
	rdb := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = rdb.Close() })
	require.NoError(t, rdb.Ping(ctx).Err())
	return rdb
}
