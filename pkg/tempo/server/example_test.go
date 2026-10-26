package chargeserver_test

import (
	"fmt"
	"os"

	"github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

func ExampleNew() {
	method, err := charge.New(charge.Config{
		ChainID:   tempo.ChainIDModerato,
		Recipient: "0x742d35Cc6634c0532925a3b844bC9e7595F8fE00",
		// Use a persistent store shared by all replicas in production.
		Store: tempo.NewMemoryStore(),
		// Sponsor gas when FEE_PAYER_PRIVATE_KEY is set, paying fees in pathUSD.
		FeePayerPrivateKey: os.Getenv("FEE_PAYER_PRIVATE_KEY"),
		AllowedFeeTokens:   []string{tempo.PathUSDAddress},
	})
	if err != nil {
		panic(err)
	}
	payment, err := server.New(method, "api.example.com", "a-32-byte-or-longer-server-secret")
	if err != nil {
		panic(err)
	}
	fmt.Println(payment != nil)
	// Output: true
}
