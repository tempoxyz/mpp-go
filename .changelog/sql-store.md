---
github.com/tempoxyz/mpp-go: patch
---

Added `github.com/tempoxyz/mpp-go/pkg/tempo/sqlstore`, a replay-protection store for the Tempo charge verifier on `database/sql` (Postgres or SQLite 3.35+), ported from mppx's `Store.sql()`. `sqlstore.New(db, sqlstore.Postgres, sqlstore.Options{})` implements `tempo.Store` and `tempo.ExpiringStore` with one table (`mppx_store` by default, created on first use unless `Options.SkipCreateTable` is set) and single-statement atomic writes: `PutIfAbsent` and `PutIfAbsentUntil` insert with `ON CONFLICT … DO UPDATE … WHERE` the existing entry has expired, so a live claim is never replaced and `Get` reports an expired one as missing. The package imports no driver. `Store.PurgeExpired` deletes claims that expired more than `Options.PurgeGrace` ago (one hour by default, to cover clock skew between processes sharing the table), which nothing else removes.
