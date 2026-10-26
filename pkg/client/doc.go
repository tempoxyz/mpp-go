// Package client provides the generic HTTP 402 retry transport used by MPP
// payment methods.
//
// NewHTTPClient returns a standard *http.Client that pays 402 Challenges and
// only follows same-origin redirects; Client and NewHTTPTransport take the
// same Options. Read the server's receipt from a paid response with
// mpp.ReceiptFromResponse.
//
// Examples import this package by its bare name. The Tempo charge package
// uses the alias `charge`, so there is no conflict.
package client
