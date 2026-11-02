---
github.com/tempoxyz/mpp-go: minor
---

**Wire break: verifiers on earlier mpp-go releases reject Challenges this release issues for scoped routes, and this release rejects theirs.** Roll every replica to this release together, or drain outstanding Challenges (5 minutes by default) first.

The route scope now uses mppx's wire format. Instead of a `_mppx_scope` `{route, resource, query}` map inside the challenge request, the server binds a `"GET /route"` string (upper-case method, space, route template, or the request path without a template) into the HMAC-bound opaque metadata as `_mppx_scope`, byte-for-byte as mppx does, so Go and mppx servers sharing a secret accept each other's scoped Challenges. As in mppx, the resource and query are no longer bound: a Credential is accepted on any resource of the route it was issued for, and rejected on another route.

API changes: `ChargeParams.MppxScope` is now a `string`, `ScopeFromHTTPRequest` returns a `string`, and `ChargeParams.WithDefaultScope` binds a request's scope only to params that set none (through `MppxScope` or `Meta["_mppx_scope"]`). Setting `MppxScope` and a different `Meta["_mppx_scope"]` is a server error, as in mppx.
