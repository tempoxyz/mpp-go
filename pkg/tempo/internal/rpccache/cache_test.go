package rpccache

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCacheReusesClients(t *testing.T) {
	t.Parallel()
	var cache Cache
	first := cache.Get("https://rpc.example")
	assert.Same(t, first, cache.Get("https://rpc.example"))
	assert.NotSame(t, first, cache.Get("https://other.example"))
	for i := range 2 * maxClients {
		cache.Get(fmt.Sprintf("https://rpc%d.example", i))
	}
	assert.Len(t, cache.clients, maxClients)

	var unset *Cache
	assert.NotSame(t, unset.Get("https://rpc.example"), unset.Get("https://rpc.example"))
}
