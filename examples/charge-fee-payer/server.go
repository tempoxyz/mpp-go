package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/tempoxyz/mpp-go/examples/internal/temposim"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	chargeserver "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

type exampleServer struct {
	url    string
	rpc    *temposim.RPC
	server *httptest.Server
}

func startServer() (*exampleServer, error) {
	request, err := tempo.NormalizeChargeRequest(tempo.ChargeRequestParams{
		Amount:    "0.50",
		ChainID:   temposim.ChainID,
		Currency:  temposim.Currency,
		Decimals:  tempo.DefaultDecimals,
		FeePayer:  true,
		Recipient: temposim.Recipient,
	})
	if err != nil {
		return nil, err
	}
	rpc := temposim.NewRPC(request)

	intent, err := chargeserver.NewChargeIntent(chargeserver.ChargeIntentConfig{
		FeePayerPrivateKey: temposim.FeePayerPrivateKey,
		RPC:                rpc,
	})
	if err != nil {
		return nil, err
	}
	method := chargeserver.NewMethod(chargeserver.MethodConfig{
		Intent:    intent,
		ChainID:   temposim.ChainID,
		Currency:  temposim.Currency,
		FeePayer:  true,
		Recipient: temposim.Recipient,
	})
	payment := mppserver.New(method, temposim.Realm, "example-secret")

	handler := mppserver.ChargeMiddleware(payment, mppserver.ChargeParams{
		Amount:      "0.50",
		Description: "Fee-payer Tempo charge example",
		FeePayer:    true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tx": mppserver.ReceiptFromContext(r.Context()).Reference,
		})
	}))

	server := httptest.NewServer(handler)
	return &exampleServer{url: server.URL, rpc: rpc, server: server}, nil
}

func (s *exampleServer) Close() {
	s.server.Close()
}
