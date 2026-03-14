# Release Process

This document describes the release process for cursortab.nvim.

## Pre-release Checklist

1. Ensure all tests pass:

   ```bash
   cd server && go test ./...
   ```

2. Test basic completion flow with at least one provider.

## Versioning

Follow semantic versioning.

**Pre-stable:** `v0.MINOR.PATCH` (e.g., `v0.7.0`, `v0.7.1`)

- Breaking changes increment MINOR (marked with `!` in commits)
- Bug fixes and features increment PATCH

**Stable:** `vMAJOR.MINOR.PATCH` (e.g., `v1.0.0`, `v1.1.0`)

- First stable release starts at `v1.0.0`
- Breaking changes increment MAJOR (marked with `!` in commits)

## Version Location

The version is defined in `server/daemon.go`:

```go
Version: "0.7.0", // AUTO-UPDATED by release workflow
```

The release workflow automatically updates this when a tag is pushed.

## Creating a Release

1. Create and push a git tag:

   ```bash
   git tag -a v0.7.0 -m "v0.7.0"
   git push origin v0.7.0
   ```

2. The release workflow will:
   - Run tests
   - Update the version in `server/daemon.go`
   - Commit and push the version update to main
   - Create the GitHub release with auto-generated notes
