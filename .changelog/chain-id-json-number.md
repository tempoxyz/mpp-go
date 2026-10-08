---
github.com/tempoxyz/mpp-go: patch
---

Keep `methodDetails.chainId` when parsing charge requests decoded from the wire. Challenge JSON is decoded with `json.Number`, which `ParseChargeRequest` did not recognize, so every parsed challenge lost its chain ID and the client chain pin and server chain checks were skipped. `TransformUnits` now accepts `json.Number` decimals as well.
