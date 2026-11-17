---
github.com/tempoxyz/mpp-go: patch
---

Changed the HTTP and MCP clients to resend the same Credential when a payment-required answer offers the Challenge just answered again, including after an invalid-payload problem, and to pay any other Challenge with a new Credential, at most three times, as mppx does. Changed the MCP client to treat only -32042 errors and -32043 errors whose httpStatus is 402 or absent as payment-required, so other errors such as invalid-params are the call's final answer.
