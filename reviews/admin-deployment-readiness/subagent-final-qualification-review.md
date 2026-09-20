# Independent qualification review

Scope: `internal/app/cli/composectl/qualification_multinode.go`,
`qualification_runtime.go`, their focused tests, and the installed-candidate
wiring. This review does not modify the implementation.

## Validation performed

- `go test ./internal/app/cli/composectl -count=1` — passed.
- `git diff --check` on the four qualification implementation/test files —
  passed.
- Inspected the shipped Compose volume layout and exercised the Docker
  `--tmpfs` plus `volume-subpath` mount shape with the locally available
  LeapView image; the Docker mount syntax is accepted by the installed Docker
  29.1 daemon.

## Original blocker (resolved 2026-09-18)

### The multi-node drill does not share the qualification physical pool or prove data-plane serving

`qualificationMultiNodeVolumes` shares only the local managed-data subpath
(`qualification_multinode.go:200-235`). The qualification physical-pool
identity is generated from `cfg.DuckLakeDataDir()` and, for the primary
Compose environment, points at `/var/lib/leapview/home/data/delivery`
(`internal/app/adminpostgres/qualification.go:47-53`). The secondary rewrites
`LEAPVIEW_HOME` to `/var/lib/leapview` but does not mount the primary
`home/data` subpath (`qualification_multinode.go:302-306`), so its private
tmpfs has no primary DuckLake data files. This is true regardless of whether
managed data is local or S3: the qualification pool itself is local.

The serving runtime loads the admitted pool contract and DuckLake `DATA_PATH`
from the native PostgreSQL authority (`internal/app/runtimefactory/postgres.go`
and `internal/analytics/ducklake/environment.go`), so a secondary can be
ready and report the same durable identity while still lacking the local
physical files needed by a governed query. The multi-node routine checks
`/readyz`, identity, and the PostgreSQL active pointer, but never performs a
governed query against the secondary after startup, after primary loss, or
after either restart (`qualification_multinode.go:111-196`). Its fixture makes
all readiness/identity operations unconditional, so the current test cannot
detect this failure (`qualification_multinode_test.go:110-180`).

This blocks claiming that the gate qualifies two independently serving
application processes against the same native PostgreSQL/DuckLake authority.
Either share the exact primary-owned local physical-pool subpath in the
secondary (while preserving private process state), or make the qualification
pool object-backed and then issue a real governed query through the secondary
at each failover boundary. Add an integration assertion that the query result
and active generation remain correct after primary loss and both restarts.

## Nice-to-haves / follow-up hardening

- Add direct tests for `qualificationMultiNodeVolumes`: local and S3 backend
  behavior, custom managed-data subpaths, invalid paths, and the physical-pool
  mount contract. The current test only asserts one local happy path.
- Add a Docker integration test (or an explicit minimum Docker Engine
  prerequisite) for `--mount ... volume-subpath=...`; the runtime test checks
  only generated argv and cannot catch daemon/version or mount-order failures.
- Exercise secondary-start/readiness failure and cleanup failure paths. In
  particular, `dockerCLIQualificationRuntime.Start` returns no handle when
  `docker run` fails, so a daemon that leaves a named container behind can
  leave the deterministic `*-node-b` name blocking a later qualification.
- Harden the disposable secondary to match the production Compose security and
  lifecycle envelope (`--init`, capability drop, no-new-privileges, and
  resource bounds), or document why this intentionally differs from the
  primary. This is qualification fidelity rather than a correctness blocker.

## Original assessment

The lifecycle sequencing, durable-pointer checks, cleanup defer, CA rewrite,
and deterministic Docker argument construction are coherent, and all focused
Go tests pass. The physical-pool/data-plane omission above must be resolved (or
the claim narrowed to control-plane/readiness convergence) before treating the
multi-node qualification as release evidence.

## Resolution — 2026-09-18

The blocker is closed. The secondary now shares the exact physical-pool,
extension-cache, and relevant object-store/managed-data volume subpaths while
retaining a private process home. Qualification issues real governed queries
through the secondary before failure, after abrupt primary loss, and after its
restart, then through the restored primary after the rolling restart. The
exact installed-candidate run passed and records `dataPlaneQueries=true`,
`durableConvergence=true`, and all other release assertions true.

The direct mount/security/failure-path items above remain useful post-release
hardening. They are not required to establish the production behavior already
proved by the exact Docker run.
