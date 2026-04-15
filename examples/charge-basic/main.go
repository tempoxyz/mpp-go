package main

import (
	"encoding/json"
	"net/http"

	genericclient "github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	genericserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	tempoclient "github.com/tempoxyz/mpp-go/pkg/tempo/client"
	temposerver "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

func main() {
	intent, _ := temposerver.NewChargeIntent(temposerver.ChargeIntentConfig{
		RPCURL: "https://rpc.moderato.tempo.xyz",
	})
	method := temposerver.NewMethod(temposerver.MethodConfig{
		Intent:    intent,
		ChainID:   42431,
		Currency:  tempo.DefaultCurrencyForChain(42431),
		Recipient: "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
	})
	payment := genericserver.New(method, "api.example.com", "replace-me")

	http.HandleFunc("/paid", func(w http.ResponseWriter, r *http.Request) {
		result, err := payment.Charge(r.Context(), genericserver.ChargeParams{
			Authorization: r.Header.Get("Authorization"),
			Amount:        "0.50",
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if result.IsChallenge() {
			w.Header().Set("WWW-Authenticate", result.Challenge.ToWWWAuthenticate("api.example.com"))
			w.WriteHeader(http.StatusPaymentRequired)
			json.NewEncoder(w).Encode(mpp.ErrPaymentRequired("api.example.com", "").ProblemDetails(result.Challenge.ID))
			return
		}
		w.Header().Set("Payment-Receipt", result.Receipt.ToPaymentReceipt())
		json.NewEncoder(w).Encode(map[string]any{"data": "paid content", "payer": result.Credential.Source})
	})

	clientMethod, _ := tempoclient.New(tempoclient.Config{
		PrivateKey: "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d",
		ChainID:    42431,
		RPCURL:     "https://rpc.moderato.tempo.xyz",
	})
	_ = genericclient.New([]genericclient.Method{clientMethod})

	_ = http.ListenAndServe(":8080", nil)
}
