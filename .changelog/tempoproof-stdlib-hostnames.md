---
github.com/tempoxyz/mpp-go: patch
---

Clients no longer depend on `golang.org/x/net` and `golang.org/x/text`: before signing a zero-amount Tempo proof, the realm check compares the request hostname with the realm without IDNA conversion, as mppx compares the realm with the URL's ASCII hostname. ASCII hostnames and realms behave as before (equal ignoring case). A hostname or realm with a non-ASCII character no longer owns a realm, so a client refuses to sign a proof for a Unicode domain unless the request URL and the realm both use its `xn--` form (previously both sides were converted with IDNA). `go list -deps ./pkg/client` now lists only `github.com/gowebpki/jcs` outside the standard library.
