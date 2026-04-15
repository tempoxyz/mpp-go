package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

type exampleServer struct {
	url    string
	server *httptest.Server
}

func startServer(rpcURL string, chainID int64) (*exampleServer, error) {
	intent, err := charge.NewChargeIntent(charge.ChargeIntentConfig{RPCURL: rpcURL})
	if err != nil {
		return nil, err
	}
	method := charge.NewMethod(charge.MethodConfig{
		Intent:    intent,
		ChainID:   chainID,
		Currency:  devnet.Currency,
		Recipient: devnet.Recipient,
	})
	payment := mppserver.New(method, devnet.Realm, "example-secret")

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
	return &exampleServer{url: server.URL, server: server}, nil
}

func (s *exampleServer) Close() {
	s.server.Close()
}
