package main

import (
	genericclient "github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	tempoclient "github.com/tempoxyz/mpp-go/pkg/tempo/client"
	temposerver "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

func main() {
	intent, _ := temposerver.NewChargeIntent(temposerver.ChargeIntentConfig{
		RPCURL:             "https://rpc.moderato.tempo.xyz",
		FeePayerPrivateKey: "0xdd83cd66cd98801a07e0b7c1a5b02364b369e696da7c0ab444acffea5cca86fc",
	})
	_ = temposerver.NewMethod(temposerver.MethodConfig{
		Intent:    intent,
		ChainID:   42431,
		Currency:  tempo.DefaultCurrencyForChain(42431),
		Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
		FeePayer:  true,
	})

	method, _ := tempoclient.New(tempoclient.Config{
		PrivateKey: "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d",
		ChainID:    42431,
		RPCURL:     "https://rpc.moderato.tempo.xyz",
	})
	_ = genericclient.New([]genericclient.Method{method})
}
