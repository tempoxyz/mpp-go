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
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	"github.com/tempoxyz/mpp-go/pkg/tempo/client"
	"github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

func main() {
	ctx := context.Background()
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:    "0.50",
		ChainID:   temposim.ChainID,
		Currency:  temposim.Currency,
		Decimals:  tempo.DefaultDecimals,
		Recipient: temposim.Recipient,
	})
	if err != nil {
		log.Fatal(err)
	}
	rpc := temposim.NewRPC(request)

	// Server side: return a 402 Challenge until a valid Credential arrives.
	intent, err := temposerver.NewChargeIntent(temposerver.ChargeIntentConfig{RPC: rpc})
	if err != nil {
		log.Fatal(err)
	}
	method := temposerver.NewMethod(temposerver.MethodConfig{
		Intent:    intent,
		ChainID:   temposim.ChainID,
		Currency:  temposim.Currency,
		Recipient: temposim.Recipient,
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
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  "paid content",
			"payer": result.Credential.Source,
		})
	}))
	defer api.Close()

	// Client side: automatically answer the 402 Challenge with a Tempo Credential.
	clientMethod, err := tempoclient.New(tempoclient.Config{
		ChainID:    temposim.ChainID,
		PrivateKey: temposim.PayerPrivateKey,
		RPC:        rpc,
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

	receipt, err := mpp.ParsePaymentReceipt(response.Header.Get("Payment-Receipt"))
	if err != nil {
		log.Fatal(err)
	}
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("paid request for %q with receipt %s\n", body["data"], receipt.Reference)
}
