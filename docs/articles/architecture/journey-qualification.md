# Cross-role journey qualification

This manifest maps consumer, creator, and operator states to maintained checks.
A state is qualified only when its named check produces the expected status,
signal, redirect, or persisted evidence in a source-bound execution receipt.
Route registration and component event assertions provide supporting evidence;
they do not establish a complete authenticated browser journey.

## Scope and evidence rules

- **Real application boundary.** Assembled checks use `assembleRuntime(...).Routes()`
  or `NewPostgresJourneyFixture`. Domain handlers, recording ports, deterministic
  serving readers, and renderer fixtures have narrower boundaries, described below.
- **Deterministic failure injection.** Use an invalid credential, revoked session,
  denied resource, missing request identity, unknown candidate, or fixed unavailable
  dependency. Do not use sleeps, random retries, or a product-masking retry loop.
- **Evidence capture.** Record the source, environment, command, exit, skips, role,
  request identity, and expected and observed status, signal, redirect, or durable
  outcome. Redact tokens, cookies, credentials, and provider diagnostics. A listed
  check without a qualifying execution receipt remains unqualified.
- **Bounded and parallel-safe.** Each check owns its database or immutable fixture
  identity. Close streams, listeners, and background workers in cleanup.
- **No hidden approval.** Candidate approval and serving activation remain explicit
  operator actions. Browser creator journeys must not auto-approve or activate.

## Maintained checks and boundaries

| Named check | Source | Observations and limits |
| --- | --- | --- |
| `TestCredentialedBrowserAndPipelineJourney` | `internal/app/credentialed_journey_integration_test.go` | One auth-enabled assembled HTTP fixture covers invalid and successful local login, session expiry, return to Sources, renewed login, logout and protected admin recovery. The connection command reaches a recording Administration port with a credential reference. Pipeline callbacks return queued, target-active, and fixed failure outcomes. These recording seams do not prove provider mutation or durable pipeline execution. |
| `TestOIDCBrowserJourneyCreatesIsolatedDurableSessions` | `internal/app/oidc_journey_test.go` | Real TLS issuer metadata, JWKS and token endpoints feed the actual OIDC client, with local issuer trust injected. Cookie-jar requests traverse authorization and the assembled callback into native PostgreSQL sessions and authenticated admin/profile access. Negative state, nonce and audience, replay, Alice/Bob isolation, and durable revocation are asserted. Provider consent UI, an actual browser renderer, and third-party issuer integration are outside this fixture. Qualification remains pending its integrated native execution receipt. |
| `TestPostgresRefreshRouteJourney` | `internal/app/postgres_refresh_journey_test.go` | Generated routes use native PostgreSQL refresh, job, event and idempotency authorities. Checks cover admission, identical replay, list/detail/events, foreign-project and missing-run responses, and the admin storage shell. Serving readers and artifact loading are deterministic seams; this is not a real browser renderer or the complete production authorization composition. |
| `TestPostgresPublicDashboardJourney` | `internal/app/postgres_public_dashboard_journey_test.go` | Native assembled public-dashboard publication and delivery journey. Keep its visibility and persistence evidence distinct from private authoring and creator permissions. |
| `TestRouteInventory` | `internal/app/route_inventory_test.go` | Enumerates every mounted route exactly once and checks owner/access/privilege metadata against the contract digest. It does not send every request or establish each role's complete journey. |
| `TestProjectAuthoringGuardRejectsDashboardReadToken` | `internal/app/project_authorization_typed_test.go` | A dashboard-read token cannot satisfy the project authoring guard. This is an authorization boundary check, not an assembled create/fork mutation journey. |
| `TestDashboardReadAuthorizationReturnsForbiddenForDeniedResource` | `internal/dashboard/http/dashboard_read_authorization_test.go` | Dashboard resource denial is explicit at the feature HTTP boundary. |
| `TestPipelineMutationProjectionRequiresResourceUse` | `internal/project/http/browser_test.go` | Pipeline mutation presentation requires resource-use authority. Presentation gating does not substitute for command authorization or durable execution. |
| `TestPipelineRunRouteFailsClosedForUnauthorizedWrongPipelineAndChildRuns` | `internal/project/http/pipeline_run_detail_test.go` | Run routes fail closed for denied resources, wrong pipeline scope, and child-run scope. |
| `TestProtectCandidateProjectResourcesRequiresAuthenticationOnFreshTarget` | `internal/app/candidate_preview_authorization_test.go` | Fresh-target candidate protection requires authentication. This exercises the protection boundary rather than a rendered review journey. |
| `TestProtectCandidateProjectResourcesDeniesFreshTargetNonAdmin` | `internal/app/candidate_preview_authorization_test.go` | Fresh-target candidate protection denies a non-admin principal. |
| `TestServeCandidateReviewRejectsIncompleteScope` | `internal/app/candidate_routes_test.go` | Incomplete review scope is rejected; production candidate service and activation checks belong to deployment qualification. |

## State coverage to reconcile

The checks above establish their stated boundaries. A complete consumer, creator,
and operator qualification must additionally bind the actual dashboard/develop
read surfaces, create/fork idempotency and source immutability, connection-service
mutation and audit, pipeline cancellation/retry and durable execution, and candidate
review/approval/activation to their owning domain receipts. Do not substitute a
recording callback or route metadata row for those outcomes.

The mounted route and keyboard harnesses complement these server checks with
actual browser signals, modal interactions, Explorer keyboard navigation, and
accessibility scans. Their source-bound receipts must retain failed readiness or
rendering budgets and explicit skipped/unavailable states. A package test pass
alone does not qualify those browser outcomes.

## Qualification command set

Run the maintained assembled HTTP and route-contract lane:

```sh
go test ./internal/app -run 'TestCredentialedBrowserAndPipelineJourney|TestOIDCBrowserJourneyCreatesIsolatedDurableSessions|TestRouteInventory' -count=1
```

With the native PostgreSQL fixture prerequisites available, run the durable
refresh and public-delivery journeys:

```sh
go test ./internal/app -run 'TestPostgresRefreshRouteJourney|TestPostgresPublicDashboardJourney' -count=1
```

Then run the supporting domain lanes and documentation contract:

```sh
go test ./internal/dashboard/http ./internal/project/http ./internal/deployment/... -count=1
task docs:check
```

Capture actual skips: a missing native prerequisite is not a successful state
qualification. Do not retry until a different state appears or weaken an expected
authorization boundary to make the manifest green.
