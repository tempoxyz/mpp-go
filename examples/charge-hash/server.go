package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	mppserver "github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
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
		Intent:         intent,
		ChainID:        chainID,
		Currency:       devnet.Currency,
		Recipient:      devnet.Recipient,
		SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
	})
	payment := mppserver.New(method, devnet.Realm, "example-secret")

	handler := mppserver.ChargeMiddleware(payment, mppserver.ChargeParams{
		Amount:         "0.50",
		Description:    "Hash credential Tempo charge example",
		SupportedModes: []tempo.ChargeMode{tempo.ChargeModePush},
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
