# APIGen upstream provenance

This directory is a temporary in-repository fork of the APIGen Go module while
LeapView and APIGen evolve the operation-contract model together.

- Source repository: https://github.com/Yacobolo/toolbelt
- Module: `github.com/Yacobolo/toolbelt/apigen`
- Imported version: `v0.7.3`
- Upstream tag: `apigen/v0.7.3`
- Upstream commit: `f862727b9fda88a101a41f272940e0ce0b2c2fd1`
- Imported from: the Go module cache for `github.com/Yacobolo/toolbelt/apigen@v0.7.3`
- Import date: 2026-08-10

The module path in `go.mod` intentionally remains unchanged. LeapView's root
module selects this source through a local `replace` directive, and generation
commands invoke the unversioned module package so both runtime imports and the
generator CLI resolve to this directory.

## Local changes

Keep LeapView-specific changes focused on generally useful APIGen capabilities
and describe them here as they are introduced. Relative to `apigen/v0.7.3`, this
fork adds the typed `@apigen.command` decorator, normalized command metadata in
IR v4, validation for stable operation IDs/audit actions/targets and HTTP
idempotency or concurrency policies, generated Go command contracts, and the
OpenAPI `x-apigen-command` projection. It also adds generated concrete-route
policy lookup and full runtime registries plus the shared command invocation,
dependency, optimistic-concurrency, completion-guard, and observation runtime.
The published Go module omits the nested
`example` module even though the root smoke tests require it, so `example/` was
copied without modification from the same upstream commit recorded above.

## Updating from upstream

1. Record the current local delta from the commit above.
2. Import the complete source for the desired `apigen/<version>` tag.
3. Reapply or merge the documented local changes.
4. Update the version, tag, commit, import date, and local-change notes above.
5. Run `task apigen:test`, `task api:generate`, and LeapView's normal CI contract.

## Returning to the published module

After the local changes are upstream and released, update LeapView's required
APIGen version, remove the root `replace` directive and this directory, then
regenerate all APIGen-owned artifacts and run the full CI contract.
