package chargeclient_test

import (
	"fmt"

	"github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/client"
)

func ExampleMaxAmount() {
	method, err := charge.New(charge.Config{
		PrivateKey: "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80",
		ChainID:    tempo.ChainIDMainnet,
		// Pay at most 0.50 OUSD per request, and only on mainnet.
		PaymentPolicy: charge.AllPolicies(
			charge.MaxAmount(tempo.OUSDAddress, "0.50", 6),
			charge.AllowChainIDs(tempo.ChainIDMainnet),
		),
	})
	if err != nil {
		panic(err)
	}
	httpClient, err := client.NewHTTPClient([]client.Method{method})
	if err != nil {
		panic(err)
	}
	fmt.Println(httpClient != nil)
	// Output: true
}
