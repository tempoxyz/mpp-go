---
github.com/tempoxyz/mpp-go: patch
---

Core API cleanup: one canonical way per operation, with the old spellings kept
as deprecated wrappers.

- `mpp.FormatChallenge(c) (string, error)` writes `c.Realm`, the realm the
  challenge ID binds, and returns formatting errors.
  `(*Challenge).VerifyID(secret)` checks an ID against `c.Realm`.
- `mpp.FormatCredential(c) (string, error)` and
  `mpp.FormatReceiptWithError(r) (string, error)` return formatting errors; the
  latter rejects receipts `ParseReceipt` would reject, such as an empty method.
  `mpp.WithExtensions` sets top-level receipt extension fields.
- `mpp.ChallengesFromResponse` and `mpp.ReceiptFromResponse` parse a
  `*http.Response`, handling repeated and merged `WWW-Authenticate` fields and
  ignoring other schemes.
- `mpp.ExpiresIn`, `mpp.ParseExpires` and `mpp.CheckExpires` replace the
  `mpp.Expires` helpers and the SDK's duplicated expiry parsers.
  `mpp.ComputeBodyDigest` and `mpp.VerifyBodyDigest` take raw bytes and never
  panic. `mpp.ParseAmount` returns a `*big.Int`, so 18-decimal amounts do not
  overflow.
- `mpp.IntentCharge` and `mpp.IntentSession` constants; `tempo.IntentCharge`
  refers to `mpp.IntentCharge`.
- `mpp.ErrorType` implements `error` and `*mpp.PaymentError` implements `Is`, so
  `errors.Is(err, mpp.ErrorTypeMalformedCredential)` works through wrapping.
- `server.WritePaymentResponse(w, err, challenges...)` and
  `fiberadapter.WritePaymentResponse(c, err, challenges...)` write initial
  402s, problem details and retry challenges, with each challenge's own realm.
  Server body digests now return an error instead of panicking for an
  unsupported `Body` such as an `io.Reader`, and the middlewares answer a
  receipt that cannot be formatted with a 500 internal payment error.

Deprecated: `FormatAuthenticate`, `FormatAuthenticateStrict`,
`(*Challenge).ToAuthenticate`, `ToAuthenticateStrict`, `Verify`,
`FromAuthenticate`, `NewChallenge`, `GenerateChallengeID`,
`FormatAuthorization`, `FormatAuthorizationStrict`,
`(*Credential).ToAuthorization`, `FromAuthorization`, `ParsePaymentReceipt`,
`FormatPaymentReceipt`, `FromPaymentReceipt`, `FormatReceipt`,
`(*Receipt).ToPaymentReceipt`, `Receipt.Extra`, `WithExtra`, `Expires`,
`BodyDigest`, `ParseUnits`, `TransformUnits`, and in `server` and
`fiberadapter` `WritePaymentError`, `WritePaymentErrorWithChallenge`,
`WritePaymentErrorWithChallenges`, `WriteChallenge` and `WriteChallenges`.
