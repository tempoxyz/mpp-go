---
github.com/tempoxyz/mpp-go: patch
---

Match client challenges on intent as well as method. The transport selected the first challenge sharing a method token, so when a server offered the same method under several intents (the core spec's intent negotiation example), it committed to an unsupported intent and failed the request instead of paying a challenge it could settle. Methods may now declare their intents via the optional `client.IntentMethod` interface, which the built-in Tempo client method implements; methods that do not implement it keep matching on the method token alone.
