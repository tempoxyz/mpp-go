---
github.com/tempoxyz/mpp-go: patch
---

Changed the HTTP and MCP clients to resend the same Credential when a payment-required answer offers the Challenge just answered again (signing a new one only after an invalid-payload problem), and to pay any other Challenge with a new Credential, at most three times, as mppx does. Changed the MCP client to treat only -32042 errors, -32043 errors whose httpStatus is 402 or absent, and -32602 errors with an invalid-payload problem as payment-required, so other errors are the call's final answer.
