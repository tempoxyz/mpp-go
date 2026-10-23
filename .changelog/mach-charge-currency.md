---
github.com/tempoxyz/mpp-go: minor
---

Added the MACH token address (`tempo.MACHAddress`) and made the Tempo charge client pay gas for MACH charges in a supported stablecoin (pathUSD, then the chain's default currency, preferring one the payer holds), since MACH cannot pay transaction fees.
