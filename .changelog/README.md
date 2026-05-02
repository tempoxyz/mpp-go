# Changelogs

This folder contains changelog files that describe changes to be released.

## Adding a changelog

Run `changelogs add` to create a new changelog file.

## File format

Changelog files are markdown with YAML frontmatter:

```markdown
---
github.com/tempoxyz/mpp-go: minor
---

Description of the changes made.
```

> **Go module naming:** the package identifier is the **full module path**
> from `go.mod` (e.g. `github.com/tempoxyz/mpp-go`), not the bare repo
> name. Using `mpp-go: minor` will not be recognized.

Bump levels: `patch`, `minor`, `major`.

## Releasing

Releases are automated. On push to `main`, the `Changelog Release` workflow
opens (or updates) a "Version Packages" PR that applies the version bump and
updates `CHANGELOG.md`. Merging that PR pushes the new `vX.Y.Z` tag, which
publishes via `proxy.golang.org`.

This module has no tags yet, so the first auto-release will start from
`0.0.0` and bump to whatever level the staged changelog requests.

To preview locally:

```bash
changelogs status
```
