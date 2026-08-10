package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/server"
)

type relayServer struct {
	*httptest.Server
	payment *server.Mpp
}

func startRelayServer(apiKey, apiURL, secretKey string, chainID int64) (*relayServer, error) {
	recipientKey, err := crypto.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("generate recipient: %w", err)
	}
	recipient := crypto.PubkeyToAddress(recipientKey.PublicKey)
	method, err := charge.MethodFromConfig(charge.Config{
		ChainID:   chainID,
		Currency:  tempo.DefaultCurrencyForChain(chainID),
		Recipient: recipient.Hex(),
		Relay: &charge.RelayConfig{
			APIKey:     apiKey,
			APIBaseURL: apiURL,
		},
		SupportedModes: []tempo.ChargeMode{tempo.ChargeModePull},
	})
	if err != nil {
		return nil, fmt.Errorf("configure relay server: %w", err)
	}
	payment, err := server.New(method, relayRealm, secretKey)
	if err != nil {
		return nil, fmt.Errorf("configure payment server: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.Handle("GET /api/photo", server.ChargeMiddleware(payment, server.ChargeParams{
		Amount:      "0.01",
		Description: "Random stock photo",
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"url": "https://picsum.photos/1024/1024",
		})
	})))
	return &relayServer{Server: httptest.NewServer(mux), payment: payment}, nil
}
