---
github.com/tempoxyz/mpp-go: minor
---

Support the Tempo `subscription` intent (draft-tempo-subscription-00), as mppx's `tempo.subscription`. Clients sign a key authorization scoped to the Challenge's amount, currency, recipient, period, expiry and server access key with `chargeclient.NewSubscription`, which requires a `ValidateRequest` to authorize what it signs. Servers issue subscription Challenges, verify the key authorization, activate the subscription with a receipt carrying its `subscriptionId`, reuse it on later requests and renew overdue periods under the same `subscriptionId` with `chargeserver.NewSubscription`, through custom `Activate`/`Renew` hooks or automatic on-chain billing with a server-owned access key. Adds `SubscriptionStore`, the `tempo.AtomicStore` interface (implemented by `MemoryStore` and `RedisStore`, whose `CompareAndSwap` writes never expire) and `mpp.IntentSubscription`.
