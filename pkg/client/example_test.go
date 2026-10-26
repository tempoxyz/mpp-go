package client_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
)

// exampleMethod pays every "example/charge" challenge.
type exampleMethod struct{}

func (exampleMethod) Name() string   { return "example" }
func (exampleMethod) Intent() string { return mpp.IntentCharge }
func (exampleMethod) CreateCredential(_ context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	return &mpp.Credential{Challenge: challenge.ToEcho(), Payload: map[string]any{"paid": true}}, nil
}

func ExampleNewHTTPClient() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			challenge, err := mpp.NewChallengeWithError("secret", r.Host, "example", mpp.IntentCharge, map[string]any{"amount": "1"})
			if err != nil {
				panic(err)
			}
			header, err := mpp.FormatChallenge(challenge)
			if err != nil {
				panic(err)
			}
			w.Header().Set("WWW-Authenticate", header)
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		fmt.Fprint(w, "paid content")
	}))
	defer server.Close()

	// A standard *http.Client: pass it to any library that takes one.
	httpClient, err := client.NewHTTPClient(
		[]client.Method{exampleMethod{}},
		client.WithMaxRetries(1),
	)
	if err != nil {
		panic(err)
	}
	resp, err := httpClient.Get(server.URL)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	fmt.Println(resp.StatusCode)
	// Read the server's receipt with mpp.ReceiptFromResponse(resp).
	// Output: 200
}
