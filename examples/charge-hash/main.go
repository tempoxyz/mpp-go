package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"

	"github.com/tempoxyz/mpp-go/examples/internal/temposim"
	mppclient "github.com/tempoxyz/mpp-go/pkg/client"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	"github.com/tempoxyz/mpp-go/pkg/tempo/client"
	"github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

func main() {
	ctx := context.Background()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:         "0.50",
		ChainID:        temposim.ChainID,
		Currency:       temposim.Currency,
		Decimals:       tempo.DefaultDecimals,
		Recipient:      temposim.Recipient,
		SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
	})
	if err != nil {
		log.Fatal(err)
	}
	rpc := temposim.NewRPC(request)

	intent, err := temposerver.NewChargeIntent(temposerver.ChargeIntentConfig{RPC: rpc})
	if err != nil {
		log.Fatal(err)
	}
	method := temposerver.NewMethod(temposerver.MethodConfig{
		Intent:         intent,
		ChainID:        temposim.ChainID,
		Currency:       temposim.Currency,
		Recipient:      temposim.Recipient,
		SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
	})
	paymentServer := mppserver.New(method, temposim.Realm, "example-secret")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := paymentServer.Charge(r.Context(), mppserver.ChargeParams{
			Authorization: r.Header.Get("Authorization"),
			Amount:        "0.50",
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if result.IsChallenge() {
			w.Header().Set("WWW-Authenticate", result.Challenge.ToWWWAuthenticate(temposim.Realm))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.Header().Set("Payment-Receipt", result.Receipt.ToPaymentReceipt())
		_ = json.NewEncoder(w).Encode(map[string]any{"tx": result.Receipt.Reference})
	}))
	defer api.Close()

	clientMethod, err := tempoclient.New(tempoclient.Config{
		ChainID:        temposim.ChainID,
		CredentialType: tempo.CredentialTypeHash,
		PrivateKey:     temposim.PayerPrivateKey,
		RPC:            rpc,
	})
	if err != nil {
		log.Fatal(err)
	}
	paymentClient := mppclient.New([]mppclient.Method{clientMethod})

	response, err := paymentClient.Get(ctx, api.URL+"/paid")
	if err != nil {
		log.Fatal(err)
	}
	defer response.Body.Close()

	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("broadcast hash credential and received receipt %s\n", body["tx"])
}
