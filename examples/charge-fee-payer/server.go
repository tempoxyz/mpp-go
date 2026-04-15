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
	intent, err := charge.NewChargeIntent(charge.ChargeIntentConfig{
		FeePayerPrivateKey: devnet.FeePayerPrivateKey,
		RPCURL:             rpcURL,
	})
	if err != nil {
		return nil, err
	}
	method := charge.NewMethod(charge.MethodConfig{
		Intent:    intent,
		ChainID:   chainID,
		Currency:  devnet.Currency,
		FeePayer:  true,
		Recipient: devnet.Recipient,
	})
	payment := mppserver.New(method, devnet.Realm, "example-secret")

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
	return &exampleServer{url: server.URL, server: server}, nil
}

func (s *exampleServer) Close() {
	s.server.Close()
}
