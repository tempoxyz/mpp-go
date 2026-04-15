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
		Recipient: temposim.Recipient,
	})
	if err != nil {
		return nil, err
	}
	rpc := temposim.NewRPC(request)

	intent, err := chargeserver.NewChargeIntent(chargeserver.ChargeIntentConfig{RPC: rpc})
	if err != nil {
		return nil, err
	}
	method := chargeserver.NewMethod(chargeserver.MethodConfig{
		Intent:    intent,
		ChainID:   temposim.ChainID,
		Currency:  temposim.Currency,
		Recipient: temposim.Recipient,
	})
	payment := mppserver.New(method, temposim.Realm, "example-secret")

	handler := mppserver.ChargeMiddleware(payment, mppserver.ChargeParams{
		Amount:      "0.50",
		Description: "Basic Tempo charge example",
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credential := mppserver.CredentialFromContext(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  "paid content",
			"payer": credential.Source,
		})
	}))

	server := httptest.NewServer(handler)
	return &exampleServer{url: server.URL, rpc: rpc, server: server}, nil
}

func (s *exampleServer) Close() {
	s.server.Close()
}
