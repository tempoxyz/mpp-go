package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/tempoxyz/mpp-go/examples/internal/devnet"
	"github.com/tempoxyz/mpp-go/pkg/server"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

type exampleServer struct {
	url    string
	server *httptest.Server
}

func startServer(rpcURL string, chainID int64) (*exampleServer, error) {
	method, err := charge.New(charge.Config{
		RPCURL:    rpcURL,
		ChainID:   chainID,
		Currency:  devnet.Currency,
		Recipient: devnet.Recipient,
	})
	if err != nil {
		return nil, err
	}
	payment := server.New(method, devnet.Realm, "example-secret")

	handler := server.ChargeMiddleware(payment, server.ChargeParams{
		Amount:      "0.50",
		Description: "Basic Tempo charge example",
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credential := server.CredentialFromContext(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  "paid content",
			"payer": credential.Source,
		})
	}))

	srv := httptest.NewServer(handler)
	return &exampleServer{url: srv.URL, server: srv}, nil
}

func (s *exampleServer) Close() {
	s.server.Close()
}
