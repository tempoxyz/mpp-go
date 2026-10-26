package server_test

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
)

// exampleMethod accepts every Credential; real servers use a payment method
// such as Tempo's.
type exampleMethod struct{ name string }

func (m exampleMethod) Name() string { return m.name }

func (m exampleMethod) Intents() map[string]server.Intent {
	return map[string]server.Intent{mpp.IntentCharge: exampleIntent{method: m.name}}
}

type exampleIntent struct{ method string }

func (exampleIntent) Name() string { return mpp.IntentCharge }

func (i exampleIntent) Verify(context.Context, *mpp.Credential, map[string]any) (*mpp.Receipt, error) {
	return mpp.Success(i.method, "0xreceipt"), nil
}

const exampleSecret = "example-secret-minimum-32-byte-secret"

func exampleCredential(challenge *mpp.Challenge) string {
	authorization, err := mpp.FormatCredential(challenge.NewCredential(map[string]any{"type": "hash", "hash": "0xabc"}))
	if err != nil {
		log.Fatal(err)
	}
	return authorization
}

func ExampleMpp_ChargeRequest() {
	payment, err := server.New(exampleMethod{name: "tempo"}, "api.example.com", exampleSecret)
	if err != nil {
		log.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res, err := payment.ChargeRequest(r, server.ChargeParams{Amount: "0.01"})
		// Writes the 402 with the Challenge or the problem details, or sets
		// the Payment-Receipt header for a verified payment.
		res.ServeHTTP(w, r)
		if err != nil || res.IsChallenge() {
			return
		}
		fmt.Fprintln(w, "paid:", res.Receipt.Reference)
	})

	unpaid := httptest.NewRecorder()
	handler.ServeHTTP(unpaid, httptest.NewRequest(http.MethodGet, "/premium", nil))
	fmt.Println(unpaid.Code)

	challenge, _ := mpp.ParseChallenge(unpaid.Header().Get(mpp.HeaderWWWAuthenticate))
	r := httptest.NewRequest(http.MethodGet, "/premium", nil)
	r.Header.Set(mpp.HeaderAuthorization, exampleCredential(challenge))
	paid := httptest.NewRecorder()
	handler.ServeHTTP(paid, r)
	fmt.Print(paid.Body.String())
	receipt, _ := mpp.ParseReceipt(paid.Header().Get(mpp.HeaderPaymentReceipt))
	fmt.Println("receipt:", receipt.Reference)
	// Output:
	// 402
	// paid: 0xreceipt
	// receipt: 0xreceipt
}

func ExampleCompose() {
	tempo, _ := server.New(exampleMethod{name: "tempo"}, "api.example.com", exampleSecret)
	stripe, _ := server.New(exampleMethod{name: "stripe"}, "api.example.com", exampleSecret)
	offers := []server.Offer{
		{Mpp: tempo, Params: server.ChargeParams{Amount: "0.01"}},
		{Mpp: stripe, Params: server.ChargeParams{Amount: "0.01"}},
	}

	// Without a Credential, every offer is returned.
	offered, err := server.Compose(context.Background(), "", offers...)
	if err != nil {
		log.Fatal(err)
	}
	for _, challenge := range offered.Challenges {
		fmt.Println("offer:", challenge.Method)
	}

	// A Credential is verified against the offer it echoes.
	paid, err := server.Compose(context.Background(), exampleCredential(offered.Challenges[1]), offers...)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("paid with:", paid.Receipt.Method)
	// Output:
	// offer: tempo
	// offer: stripe
	// paid with: stripe
}

func ExampleWithEvents() {
	payment, _ := server.New(exampleMethod{name: "tempo"}, "api.example.com", exampleSecret,
		server.WithEvents(server.Events{
			ChallengeCreated: func(_ context.Context, challenge *mpp.Challenge) {
				fmt.Println("challenge created for", challenge.Method)
			},
			PaymentSucceeded: func(_ context.Context, _ *mpp.Credential, receipt *mpp.Receipt) {
				fmt.Println("payment succeeded:", receipt.Reference)
			},
			PaymentFailed: func(_ context.Context, _ *mpp.Credential, err error) {
				fmt.Println("payment failed")
			},
		}),
	)
	res, _ := payment.Charge(context.Background(), server.ChargeParams{Amount: "0.01"})
	_, _ = payment.Charge(context.Background(), server.ChargeParams{
		Amount:        "0.01",
		Authorization: exampleCredential(res.Challenge),
	})
	// Output:
	// challenge created for tempo
	// payment succeeded: 0xreceipt
}

func ExampleWithChallengeNonce() {
	payment, _ := server.New(exampleMethod{name: "tempo"}, "api.example.com", exampleSecret,
		server.WithChallengeNonce(true),
	)
	params := server.ChargeParams{Amount: "0.01", Expires: mpp.ExpiresIn(5 * time.Minute)}
	first, _ := payment.Charge(context.Background(), params)
	second, _ := payment.Charge(context.Background(), params)
	fmt.Println(first.Challenge.ID != second.Challenge.ID)
	// Output: true
}
