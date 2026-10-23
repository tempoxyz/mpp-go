---
github.com/tempoxyz/mpp-go: patch
---

Widened the sponsored (fee-payer) Tempo charge validity window and enforced the 15 second floor on both sides, to absorb clock skew between clients and servers. The client now sets the expiring-nonce `valid_before` to now + 60 seconds (`tempo.SponsoredValidBeforeWindow`, was 25), still capped at the second after the challenge expiry; against the server's 15 second minimum remaining validity this leaves about 45 seconds for clock skew, signing and round trips. The client refuses, before signing or broadcasting, a sponsored challenge that expires less than 15 seconds from now (`ErrSponsoredChallengeExpiresTooSoon`), and the Tempo server method refuses to issue a sponsored charge challenge whose `Expires` is less than 15 seconds after issuance. `tempo.FeePayerWindow` is deprecated in favour of `tempo.SponsoredValidBeforeWindow`. Clients should upgrade alongside servers.
