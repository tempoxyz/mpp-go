---
github.com/tempoxyz/mpp-go: patch
---

Stopped reading `HOST` and `HOSTNAME` in `DetectRealm`. Container runtimes set `HOSTNAME` per replica, so replicas issued challenges under different realms and rejected each other's credentials. Set `MPP_REALM` to keep a host-based realm.
