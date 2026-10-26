// Package rpccache reuses the Tempo JSON-RPC clients that the charge client
// and server build from URLs, so each is built once rather than per request.
package rpccache

import (
	"sync"

	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// maxClients bounds the cache; URLs past it get a fresh client each time.
const maxClients = 16

// Cache builds one Tempo RPC client per URL and reuses it. The zero value is
// ready to use, and a nil *Cache builds a fresh client on every Get.
type Cache struct {
	mu      sync.Mutex
	clients map[string]tempo.RPCClient
}

// Get returns the client for rpcURL, building it on first use.
func (c *Cache) Get(rpcURL string) tempo.RPCClient {
	if c == nil {
		return tempo.NewRPCClient(rpcURL)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if client, ok := c.clients[rpcURL]; ok {
		return client
	}
	client := tempo.NewRPCClient(rpcURL)
	if len(c.clients) < maxClients {
		if c.clients == nil {
			c.clients = make(map[string]tempo.RPCClient)
		}
		c.clients[rpcURL] = client
	}
	return client
}
