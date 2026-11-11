---
github.com/tempoxyz/mpp-go: patch
---

Added `pkg/tempo/sqlstore`, a replay-protection store on `database/sql` (Postgres or SQLite 3.35+) ported from mppx's `Store.sql()`, with single-statement atomic writes that never replace a live claim. `Store.PurgeExpired` deletes claims expired longer than `Options.PurgeGrace` (one hour by default).
