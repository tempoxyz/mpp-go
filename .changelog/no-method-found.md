---
github.com/tempoxyz/mpp-go: minor
---

The HTTP client now fails a request whose first 402 offers no Challenge a configured method can pay (including one whose `OrderChallenges` hook returns none) with a `*client.NoMethodFoundError`, matching `client.ErrNoMethodFound` and carrying mppx's message (`No method found for challenges: tempo.charge. Available: ...`), instead of returning the 402. A 402 answering a paid retry is still returned, as in mppx.
