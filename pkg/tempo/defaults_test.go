package tempo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	tempotx "github.com/tempoxyz/tempo-go/pkg/transaction"
)

func TestDefaultCurrencyForChain(t *testing.T) {
	t.Parallel()

	if got := DefaultCurrencyForChain(tempotx.ChainIdMainnet); got != MainnetUSDCAddress {
		assert.Failf(t, "", "DefaultCurrencyForChain(mainnet) = %q, want %q", got, MainnetUSDCAddress)
		return
	}
	if got := DefaultCurrencyForChain(tempotx.ChainIdModerato); got != tempotx.AlphaUSDAddress.Hex() {
		assert.Failf(t, "", "DefaultCurrencyForChain(moderato) = %q, want %q", got, tempotx.AlphaUSDAddress.Hex())
		return
	}
	if got := DefaultCurrencyForChain(999999); got != tempotx.AlphaUSDAddress.Hex() {
		assert.Failf(t, "", "DefaultCurrencyForChain(unknown) = %q, want %q", got, tempotx.AlphaUSDAddress.Hex())
		return
	}
}

func TestInferChainIDFromRPCURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		rpcURL string
		want   int64
	}{
		{name: "mainnet exact", rpcURL: tempotx.RpcUrlMainnet, want: tempotx.ChainIdMainnet},
		{name: "mainnet trailing slash", rpcURL: tempotx.RpcUrlMainnet + "/", want: tempotx.ChainIdMainnet},
		{name: "moderato exact", rpcURL: tempotx.RpcUrlModerato, want: tempotx.ChainIdModerato},
		{name: "moderato path", rpcURL: tempotx.RpcUrlModerato + "/rpc", want: tempotx.ChainIdModerato},
		{name: "unknown", rpcURL: "https://rpc.example.com", want: 0},
		{name: "empty", rpcURL: "", want: 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			{

				got := InferChainIDFromRPCURL(tt.rpcURL)
				if !assert.Equalf(t, tt.want, got,
					"InferChainIDFromRPCURL(%q) = %d, want %d", tt.rpcURL, got, tt.want) {
					return
				}
			}

		})
	}
}

func TestRPCURLForChain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		chainID int64
		want    string
		wantErr string
	}{
		{name: "mainnet", chainID: tempotx.ChainIdMainnet, want: tempotx.RpcUrlMainnet},
		{name: "moderato", chainID: tempotx.ChainIdModerato, want: tempotx.RpcUrlModerato},
		{name: "zero defaults to mainnet", chainID: 0, want: tempotx.RpcUrlMainnet},
		{name: "unknown", chainID: 999999, wantErr: "unknown chain id 999999"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := RPCURLForChain(tt.chainID)
			if tt.wantErr != "" {
				if !assert.Falsef(t, err == nil || err.Error() != tt.wantErr,
					"RPCURLForChain(%d) error = %v, want %q", tt.chainID, err, tt.wantErr) {
					return
				}

				return
			}
			if !assert.NoErrorf(t, err,
				"RPCURLForChain(%d) error = %v", tt.chainID, err) {
				return
			}
			if !assert.Equalf(t, tt.want, got,
				"RPCURLForChain(%d) = %q, want %q", tt.chainID, got, tt.want) {
				return
			}

		})
	}
}
