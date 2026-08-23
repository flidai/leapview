# Cross-role journey qualification

This manifest is the executable qualification contract for Linear FAI-492.
It maps the consumer, creator, and operator states that must be observed in a
real assembled application. A row is qualified only when its named check
produces the expected status, signal, redirect, or persisted evidence.

## Scope and evidence rules

- **Real application boundary.** Checks use `assembleRuntime(...).Routes()` or
  the deployed qualification image. Feature-handler unit tests are supporting
  evidence, never a substitute for assembled-router coverage.
- **Deterministic failure injection.** Use a missing/invalid bearer token, a
  viewer role, a missing CSRF token, a missing `X-Request-ID`, an unknown
  candidate, or a fixed unavailable dependency. Do not use sleeps, random
  retries, network flakiness, or a product-masking retry loop.
- **Evidence capture.** Record route, method, role, request identity, expected
  outcome, observed status/signal/redirect, and (for mutations) the durable
  audit or idempotency result. Redact bearer tokens, cookies, credentials, and
  provider diagnostics. Attach the focused `go test` output or qualification
  artifact digest to the run record.
- **Bounded and parallel-safe.** Each check owns a temporary database and
  immutable fixture IDs. Tests may run in parallel only when they do not share
  a database, target binding, idempotency key, or candidate. Keep streams and
  background workers bounded and close them in test cleanup.
- **No hidden approval.** Candidate approval and serving activation are
  intentional headless/operator-only steps. Browser creator journeys stop at a
  private draft or candidate review handoff; they must not auto-approve,
  publish, or activate a generation.

## State manifest

| Actor / state | Required success and failure observations | Real-app check | Supporting ownership | Evidence / injection |
| --- | --- | --- | --- | --- |
| Consumer, signed out | Protected dashboard, develop, candidate, and command routes reject the request; public documents remain reachable only where configured. | `internal/app/journey_qualification_integration_test.go` (`TestJourneyQualificationAssembledRouter`) and the route inventory contract. | App router + access module | Invalid bearer (`401`) and no-credential state-changing request (`403` CSRF); capture status and `Location` without credentials. |
| Consumer, project viewer | Read surfaces render only authorized graph resources; creator, review, connection mutation, and pipeline execution are denied before mutation. | Assembled router test viewer cases; dashboard/project browser route tests. | Access snapshot + project guards + domain authorizers | Fixed viewer principal in a private test DB; assert `403` or forbidden signal and zero mutation/audit rows. |
| Consumer, authorized reader | Dashboard document/page, catalog, connection detail, pipeline detail, and candidate owner preview use the active serving generation and bounded signals. | Existing dashboard stream, project browser, and candidate preview integration tests; route inventory digest. | Dashboard/project HTTP + runtime host | Fixed active generation and snapshot; capture response status, signal root, generation ID, and query audit. |
| Creator, dashboard create | `/dashboards/new` is authenticated/project-edit guarded, renders a CSRF field and fresh idempotency key, and creates a private draft through the composed authoring application. The generated form key is the stable retry identity consumed by the authoring service. | `TestJourneyQualificationAssembledRouter` create page, CSRF rejection, missing request identity, and successful create assertions; authoring service idempotency tests cover replay semantics. | Dashboard module + authoring application + app router | Missing CSRF and missing form key are deterministic failures; capture the generated key contract and resulting draft redirect. |
| Creator, dashboard fork | `/dashboards/{dashboard}/fork` requires target project edit plus source dashboard view/edit, renders a bounded form, and preserves source immutability. | Assembled fork-page check plus dashboard authoring fork tests; a published-source fixture is required for the mutation lane. | Dashboard module + source adapter + authoring service | Fixed published source; capture new draft ID, source revision, provenance, and unchanged source revision. |
| Creator, connection administration | Configuration/lifecycle commands require the generated operation claim, CSRF for cookie sessions, a stable `X-Request-ID`, and connection-scoped authorization; a valid command reaches the assembled browser's Administration port. | `TestJourneyQualificationAssembledRouter` connection command gate/role cases uses a recording port; connection administration integration tests cover the real service. | Project browser + analytics connection administration | Missing request ID and viewer role are deterministic; capture signal status and recording-port call. Real-service lanes additionally capture audit action, target/binding revision, and idempotency identity. |
| Creator, pipeline command | Run/retry/cancel requires generated claim, CSRF, stable `X-Request-ID`, pipeline `RESOURCE_USE`, and a callback boundary carrying the active request identity. | Assembled pipeline command lane (recording callback) and refresh visibility integration tests for the real active-generation callback. | Project browser + refresh module + runtime host | Missing CSRF/request ID and viewer denial are deterministic; capture callback invocation, queued/cancelled signal, and request identity. Real-service lanes capture run ID, serving identity, and audit row. |
| Operator, candidate review | Review route requires authentication and project edit. The lightweight assembled app reports a bounded dependency diagnostic when deployment composition is intentionally absent; the production deployment review handler and unknown-candidate `404` are covered by deployment candidate tests. | `TestJourneyQualificationAssembledRouter` candidate guard/availability cases; deployment candidate tests for the production handler. | App candidate routes + deployment module | Fixed unknown candidate (no retry); capture `403` viewer and `503` lightweight dependency result, or `404` in the production qualification image, plus request identity. |
| Operator, approval / activation | Candidate approval, publish, and serving-generation activation are explicit headless/operator actions. Browser review cannot mutate approval or activation state. | Deployment lifecycle, sealed publication, and CLI qualification tests; route inventory must list the headless commands. | Deployment/release/runtime-host owners | Inject unavailable gate, stale candidate, or stale target revision; capture durable rejection and unchanged active generation. |
| Recovery / failure | Runtime, connection provider, refresh worker, or storage failure produces a bounded public error and audit without leaking secrets or retrying indefinitely. | Existing lifecycle, connection binding, refresh, and runtime-host failure suites. | Owning domain service + app transport | Deterministic fake error/closed lease; capture stable public code, audit outcome, and bounded worker completion. |

## Qualification command set

Run the focused assembled lane first:

```sh
go test ./internal/app -run 'TestJourneyQualificationAssembledRouter|TestRouteInventory' -count=1
```

Then run the supporting domain lanes and documentation contract:

```sh
go test ./internal/dashboard/http ./internal/project/http ./internal/deployment/... -count=1
task docs:check
```

Any unavailable state must remain a visible failed row with its captured
diagnostic. Do not turn the row green by skipping the request, retrying until a
different state appears, or weakening the expected authorization boundary.
