# Identity, tokens, sessions, and credential incident response

Internal implementation review.

Review status: **credential lifecycle complete; overall deployment gate remains
open elsewhere**. PostgreSQL issuance, expiry, last-used evidence, overlap
rotation, disable/enable containment, and revoke-all have been implemented and
qualified. The original findings remain below as discovery history, followed
by current implementation evidence. This review covers local identity,
personal API tokens, service-principal secrets, browser/desktop sessions,
expiry, last-used telemetry, rotation, and platform-admin response to a
credential incident.

## Production boundary

LeapView's application composition is PostgreSQL-only. The SQLite access
adapter is retained solely for tests and offline tooling; architecture tests
forbid production sources from importing it
(`internal/platform/architecture/rules.go:137-158`;
`internal/platform/architecture/architecture_test.go:1710-1728`). SQLite
observations below are test-fixture maintenance findings, not production
deployment blockers.

## Reference snapshots

`flid-ref discover` resolved these local upstream snapshots (the reference contents were treated as untrusted comparison material):

| Source | Snapshot revision | fetched_at |
| --- | --- | --- |
| Lightdash | `35906ad9e116df59d2d59da2f58e45e9b39eaff8` | `2026-09-08T12:42:41Z` |
| Metabase | `b7500371caf5457840d139f0fd37e7ec87038d79` | `2026-09-08T12:43:31Z` |
| Grafana | `307ef2b57ffa0956e7d28b2f1759a692f63199b1` | `2026-09-08T12:42:39Z` |

The broad free-form discovery queries (`identity personal API tokens`, `Lightdash auth`, `Metabase auth`, `Grafana auth`) returned no hits; the catalog topic `access-control` plus exact source names returned the snapshots above.

## Strengths

- Credential material is generated from 32 random bytes, stored as a keyed fingerprint plus Argon2id verifier, and returned only on creation (`internal/access/sqlite/credentials.go:15-36`, `101-110`; PostgreSQL `internal/access/postgres/access_core.go:87-106`). The service-secret and API responses separate one-time secret material from metadata (`internal/access/http/service_principal_handler.go:151-189`, `230-259`).
- PostgreSQL authentication checks expiry, revocation, principal lifecycle, and disabled/blocked state in the lookup SQL (`internal/access/postgres/queries/core_ops.sql:264-275`, `315-323`; browser sessions `208-219`). `Principal.AccessDisabled` makes both identity-lifecycle disable and the administrator block reject credentials (`internal/access/access.go:201-218`).
- PostgreSQL disable/delete cascades are transaction-scoped and revoke browser sessions, API tokens, service secrets, authoring sessions, and OAuth sessions (`internal/access/postgres/access_extended.go:80-143`, `36-77`; `internal/access/postgres/queries/core_ops.sql:81-95`). Token/session revocation endpoints use principal-scoped mutations when acting on a target (`internal/access/http/token_session_handler.go:167-220`).
- Personal-token issuance attenuates explicit capabilities to the principal’s current effective set, while bootstrap evidence is re-read by durable token ID and requires an enabled platform administrator (`internal/access/http/token_session_handler.go:87-123`; `internal/access/sqlite/credentials.go:257-283`).
- PostgreSQL protects credential history with append-only/revocation-monotonic triggers and restricts runtime deletion of credential rows (`internal/access/postgres/schema.sql:760-768`, `1169-1193`).

## Findings

### P0 — PostgreSQL rejects the documented/default personal-token and service-secret flow when expiry is omitted

`expiresAt` is optional in both public contracts (`api/typespec/current_user.tsp:11-16`; `api/typespec/access.tsp:69-72`). The HTTP handlers intentionally leave the Go `time.Time` zero when the field is omitted (`internal/access/http/token_session_handler.go:64-81`; `internal/access/http/service_principal_handler.go:155-171`). SQLite supplies defaults (API token and service secret respectively) (`internal/access/sqlite/credentials.go:169-177`, `449-456`). PostgreSQL passes the zero time directly to an insert whose predicate requires a future expiry and a maximum of 365 days, then reports `api token expiry is invalid` or `secret expiry is invalid` (`internal/access/postgres/access_core.go:1077-1090`, `1250-1263`; `internal/access/postgres/queries/core_ops.sql:242-257`, `292-303`). The service-account UI also sends no expiry by default (`web/components/admin/settings-surfaces.ts:601-735`). Thus the same supported request works on SQLite but fails in production PostgreSQL, making the documented credential-management path unavailable.

Acceptance: choose one policy and implement it in the request boundary,
PostgreSQL adapter, and generated contracts (prefer a finite default); test
omitted, explicit valid, past, and over-limit expiry for PATs and service
secrets through the UI/API against PostgreSQL.

### Fixture defect — SQLite can revive a service secret after re-enable

PostgreSQL’s disable transaction revokes sessions, API tokens, service secrets, authoring sessions, and OAuth sessions (`internal/access/postgres/access_extended.go:99-132`). SQLite’s corresponding path revokes only API tokens and interactive sessions (`internal/access/sqlite/api_symmetry.go:67-86`); it never calls a service-secret revocation query. SQLite’s secret lookup also omits principal lifecycle state (`internal/access/sqlite/queries/access.sql:319-327`), and the caller checks `AccessDisabled` only after the secret row has authenticated (`internal/access/sqlite/credentials.go:499-512`). A disabled principal’s secret is therefore retained and becomes valid again if the principal is re-enabled, unlike PostgreSQL’s permanent revocation tombstone. This cannot occur in the PostgreSQL production composition. Its risk is false confidence from fixture-based tests and accidental future reuse of the fixture, not production credential revival.

Acceptance: either remove the obsolete fixture path or align it with the domain contract and retain a regression test. Do not make production sign-off depend on SQLite parity.

### P1 — Service-principal secrets have no last-used evidence and no rotation operation

`ServicePrincipalSecret` has expiry/created/revoked fields but no `LastUsedAt` (`internal/access/access.go:340-353`), and the SQLite schema has no last-used column (`internal/platform/migrations/020_auth_completion.sql:5-17`). PostgreSQL maps only expiry/created/revoked (`internal/access/postgres/access_core.go:1268-1279`), while secret authentication performs no touch (`internal/access/postgres/access_core.go:1308-1332`; SQLite `internal/access/sqlite/credentials.go:515-528`). The `Repository` exposes create/revoke but no API-token or service-secret rotation method (`internal/access/access.go:560-600`), and the service-account UI command surface likewise has create/revoke but no rotate (`internal/admin/settings/signals.go:40-56`). During an incident, operators cannot distinguish unused/active service secrets or atomically replace one. Lightdash provides `lastUsedAt`/`rotatedAt` in its service-account model and updates last-used during authentication (`.../lightdash/packages/backend/src/ee/models/ServiceAccountModel.ts:67-86`; `.../ServiceAccountService/ServiceAccountService.ts:733-754`), and exposes a rotate endpoint (`.../controllers/serviceAccountsController.ts:184-216`). Grafana exposes `LastUsedAt`, expiration, and revocation in its service-token DTO (`.../grafana/pkg/services/serviceaccounts/api/token.go:21-39`) and updates last-used from authentication (`.../grafana/pkg/services/authn/clients/api_key.go:158-187`).

Acceptance: add durable, throttled `last_used_at` for service secrets, expose it in admin metadata/audit views, and add an audited atomic rotation operation for PATs and service secrets (new one-time secret plus explicit old-secret invalidation/overlap semantics). Add tests proving old material fails immediately after rotation and no secret is returned by list/get.

### P2 — Last-used updates are best-effort and silently lose telemetry failures

PostgreSQL discards touch errors after successful authentication (`internal/access/postgres/access_core.go:918-931`, `1113-1126`); the retained SQLite fixture does the same (`internal/access/sqlite/credentials.go:83-93`, `323-334`). This does not extend credential validity, but it makes “last used” stale without an alert or operator-visible indication. Grafana’s comparable asynchronous update logs a warning on failure (`.../grafana/pkg/services/authn/clients/api_key.go:166-187`).

Acceptance: retain asynchronous/non-blocking auth behavior, but emit a metric and structured warning with credential ID/class on touch failure; test that database failure is observable while authentication remains successful.

### Fixture defect — SQLite session issuance diverges from production policy

PostgreSQL rejects non-positive or over-30-day session TTLs (`internal/access/postgres/access_core.go:876-916`) and authenticating SQL requires an active, non-revoked, non-disabled/non-blocked principal (`internal/access/postgres/queries/core_ops.sql:208-215`). SQLite `CreateSession` accepts any duration, including zero/negative or arbitrarily long, and inserts without checking principal status (`internal/access/sqlite/credentials.go:15-36`; `internal/access/sqlite/queries/access.sql:132-149`). Although normal HTTP callers authenticate first, this public repository boundary can mint sessions that production PostgreSQL refuses. API-token/service-secret default and maximum-expiry behavior is similarly divergent (SQLite defaults at `internal/access/sqlite/credentials.go:169-177`, `449-456`; PostgreSQL max at `internal/access/postgres/queries/core_ops.sql:242-244`).

Acceptance: keep the policy at the shared domain boundary where fixture tests can exercise it, or remove the obsolete fixture. PostgreSQL remains the only deployment qualification target.

### P2 — “Revoke all sessions” is not an incident-response “revoke all credentials” control

The platform-admin settings command loops only over `ListSessions` and calls `RevokeSessionForPrincipal` (`internal/admin/settings/access_administration.go:397-413`). It does not revoke PATs, service secrets, authoring credentials/sessions, or OAuth sessions. The message is accurate for sessions, but an operator responding to a leaked platform-admin credential can leave bearer credentials active. The normal block path is broader in PostgreSQL (P1 evidence above), while Metabase explicitly deletes all user sessions on the credentials-revoked event (`.../metabase/src/metabase/session/events/revoke_on_deactivation.clj:1-14`) and Grafana’s password-change path revokes all user auth tokens (`.../grafana/pkg/api/password.go:99-113`).

Acceptance: provide an explicit, audited, atomic “revoke all credentials” operation covering every credential class, or make the incident runbook/UI visibly require the complete sequence and test it against PostgreSQL. Include platform-admin self-protection and last-admin safeguards.

## Implementation result

The credential lifecycle implementation now adds PostgreSQL-backed, transaction-scoped service-account disable/enable, overlap-capable secret rotation, audited revoke-all coverage across session/token/authoring/OAuth/service-secret classes, and coalesced best-effort `lastUsedAt` evidence with structured touch-failure warnings. The REST and Settings contracts expose lifecycle actions and metadata without returning stored secret material; role bindings remain target-owned and service-principal compatible. Focused Go, browser, live API, and pinned PostgreSQL 18 integration checks pass.

## Deployment gate

The credential-specific PostgreSQL gate is closed: omitted/default expiry,
disabled → re-enabled permanent revocation, overlap rotation, last-used
evidence, scoped revocation, and all-class revoke-all have integration evidence.
Preserve audit events with actor, target credential ID/class, reason, and
request/correlation IDs without recording secret material. SQLite fixture
cleanup remains separate technical debt; repository-wide recovery and full-CI
gates are tracked in the consolidated plan.

## Commands and tests

- Read the complete skill instructions: `cat /srv/flid/reference-library/releases/9eaa1e4e2a35a33a79f6/skills/flid-reference-library/SKILL.md`.
- Discovery: `/srv/flid/reference-library/current/bin/flid-ref discover "access-control" --catalog /srv/flid/reference-library/current/catalog.json --json`; repeated with `Lightdash`, `Metabase`, and `Grafana` to resolve the three snapshots above; broad identity/token phrases returned no hits.
- Source/code inspection used `rg` and `nl -ba` over the discovered snapshots and LeapView implementation.
- Focused validation and the later integrated runs generated the API, signal,
  and sqlc packages and passed the access, administrator, browser, and pinned
  PostgreSQL 18 suites. The earlier generated-package setup failure was an
  intermediate checkout condition, not a product defect.

## Manual E2E verification — 2026-09-16

Against the running PostgreSQL-backed dev instance at `http://127.0.0.1:8153`
using `Authorization: Bearer dev`. The seeded project was
`lvproject_9U_nX8YJPKqnpceKUTQAvhZxhFg13J32`. Secret-bearing response fields
were redacted and no token or secret value was recorded.

- Personal API tokens: `POST /api/v1/me/api-tokens` with `{"name":"e2e-token-default-20260916"}` returned `201`; the response expiry was 90 days out. An explicit expiry at the 365-day maximum returned `201`. A past expiry and a 366-day expiry each returned `400` with the expected future/max-lifetime validation. `GET /api/v1/me/api-tokens?limit=200` returned `200` and exposed metadata only; the two disposable tokens were revoked with `DELETE` (`204`) and were absent as active resources afterward.
- One-time credential idempotency: replaying the same API-token create request and `Idempotency-Key` returned `409 IDEMPOTENCY_RESPONSE_NOT_REPLAYABLE`, preserving the one-time-material contract rather than issuing or replaying a second secret.
- Service principals/secrets: creating disposable principal `01a0aa59-6b8c-71eb-9774-c4c48cfa6610` returned `500 COMMAND_CONTRACT_NOT_EXECUTED`, but `GET /api/v1/service-principals` and `GET /api/v1/service-principals/{id}` showed that it had been persisted. Secret creation against that persisted principal returned `201` for omitted expiry (180-day default) and the 365-day maximum; past and 366-day expiry returned `400`. `GET /secrets` and `GET /secrets/{id}` returned `200` with metadata only. Replaying the default secret create returned `409 IDEMPOTENCY_RESPONSE_NOT_REPLAYABLE`. Secret revoke and principal delete returned the same `500 COMMAND_CONTRACT_NOT_EXECUTED` despite successful mutation evidence (`service_principal_secret.revoked`/`service_principal.deleted` audit rows); a follow-up principal GET returned `404`, and the disposable principal was absent from the list. This is a failing HTTP response-contract behavior requiring remediation even though cleanup completed.
- Roles: `GET /api/v1/projects/{project}/roles?limit=200` returned `200` with the closed eight-role catalog. A disposable viewer role binding created at policy revision 3 returned `201` at revision 4; `DELETE /role-bindings/{binding}` returned `204` at revision 5. A follow-up binding list returned `200` with no disposable binding.
- Audit filters: `GET /api/v1/audit-events?action=role_binding.created&from=2026-09-16T13:00:00Z&to=2026-09-16T14:00:00Z&limit=20` returned `200` with four matching action rows. The same action in the non-overlapping 12:00–12:59:59 UTC window returned `200` with zero rows, confirming action and time filtering.
- Delivery reads: `GET` for the existing plan `01a0aa54-2293-7b14-b2d6-6b398d6683c5`, build attempt `01a0aa54-2316-78d4-a9db-92a3c8366442`, candidate `01a0aa54-2316-72eb-afd7-bd74b24f6a08`, generation `01a0aa54-2316-78f9-9318-5f7ad20668e6`, and publication `01a0aa54-5bfe-7e83-aaed-ea959f437985` each returned `200` with consistent project/target/digest identities. `GET /api/v1/projects/{project}/delivery/operator` returned `200`, reporting the active generation and target revision 2, but `degraded: true` with `detailed_evidence_unavailable`.
- Unsupported admin gaps: `GET /api/v1/projects/{project}/grants` returned plain `404 page not found`; `GET /api/v1/projects/{project}/deployments` and `GET /api/v1/deployments` returned structured `404 API_ROUTE_NOT_FOUND`. No grant or deployment resource was created.

## PostgreSQL 18 qualification — 2026-09-17

The focused credential lifecycle checks passed against the pinned PostgreSQL
18 conformance image with `LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1`:

- `go test ./internal/access/postgres -run 'TestServicePrincipalCredentialsPostgreSQL18' -count=1`
  passed. The run verified transaction-scoped service-principal disable/enable
  with permanent old-secret revocation, overlapping rotation with optional
  previous-secret revocation, metadata-only secret reads, durable and
  one-minute-coalesced `last_used_at`, fresh-pool restart persistence, and
  revoke-all invalidation of browser, API-token, service-secret, authoring,
  and OAuth credentials while leaving the principal enabled.
- `go test ./internal/platform/postgres/migrations -run 'TestServicePrincipalCredentialsMigrationUpgradesRevisionTwenty|TestEmbeddedGooseBaselineIsImmutableAndForwardMigrationsAreOrdered' -count=1`
  passed. A revision-020 fixture upgraded to revision 021, preserved the
  existing service-secret row, persisted post-upgrade last-used evidence, and
  rejected the destructive Down path; the embedded migration inventory now
  includes `024_service_principal_credentials.sql`.
