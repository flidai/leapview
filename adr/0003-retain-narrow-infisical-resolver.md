# ADR-0003: Retain the narrow Infisical resolver

Status: accepted

Decision date: 2026-07-31

Implementation: complete

Deciders: LeapView maintainers

LeapView keeps its narrow Infisical resolver instead of adopting the official Go SDK. The SDK covers basic Universal Auth, OIDC login, access-token renewal, and self-hosted base URLs, but it cannot preserve LeapView's transport and credential-handling policies without retaining most of the current implementation around it.

## Required invariants

The target-owned resolver must:

- reject a project, environment, or secret path outside the operator allowlist before authenticating or contacting Infisical;
- accept only an exact HTTPS origin and never forward a bootstrap secret or bearer token across a redirect;
- use the application-injected HTTP client and TLS policy;
- propagate each resolution context through authentication and secret retrieval;
- enforce bounded authentication and secret response bodies before decoding;
- map denial, missing data, rate limiting, invalid bundles, and provider outages to stable LeapView errors without returning provider response values;
- fetch either the current or one explicitly requested historical v4 secret version, verify the returned secret ID/version, validate its non-empty JSON credential bundle, and construct one bounded-lifetime snapshot;
- refresh deterministically with an injected clock, invalidate a rejected token, and retry authentication at most once;
- obtain a fresh workload identity token when OIDC re-authentication is required; and
- work with a TLS-protected self-hosted or air-gapped Infisical instance without contacting unrelated cloud services.

## SDK v0.8.0 evaluation

| Area | Evidence | Fit |
|---|---|---|
| Universal Auth | `UniversalAuthLogin` exchanges a client ID and secret and retains them for re-authentication. | Partial |
| OIDC Auth | `OidcAuthLogin` exchanges a caller-supplied JWT, but OIDC is absent from the SDK's re-authentication strategy map. The lifecycle can renew a token but cannot obtain a fresh workload JWT when re-authentication is required. | No |
| Secret retrieval | `Secrets().Retrieve` calls deprecated `GET /api/v3/secrets/raw/{key}`. Infisical's current API and LeapView use `GET /api/v4/secrets/{key}`. | No |
| HTTP policy | Public configuration does not accept an `http.Client` or `RoundTripper`. Resty is created internally, follows redirects, and buffers response bodies. A probe confirmed that a `307 Temporary Redirect` forwards the Universal Auth JSON body, including the bootstrap secret, to the redirect target. | No |
| Cancellation and time | The constructor context stops the background refresh loop, but auth and secret methods do not accept a context. Token timing uses `time.Now` and `time.Since` directly. | No |
| Bounds and errors | There is no response-size limit. API errors parse and include the provider's response `message`, including the complete body for status 422. | No |
| Retry behavior | Network retries and randomized backoff are enabled by default. They are SDK-owned rather than coordinated with LeapView's per-resolution deadline and deterministic retry policy. | No |
| TLS and self-hosting | A self-hosted URL and custom CA certificate are supported. The URL is not required to be HTTPS by the SDK, so LeapView validation would still be required. | Partial |

## Measured impact

Measurements used Go 1.25.12 on Darwin arm64, `github.com/infisical/go-sdk` v0.8.0, and stripped binaries built with `-trimpath -ldflags="-s -w"`.

| Measurement | Without SDK | With SDK | Delta |
|---|---:|---:|---:|
| Minimal executable | 1,060,658 bytes | 17,982,450 bytes | +16,921,792 bytes |
| LeapView executable | 120,313,410 bytes | 121,190,978 bytes | +877,568 bytes |
| LeapView module graph | 585 modules | 594 modules | +9 modules |

The nine added module roots include Resty, the Infisical SDK, the OCI SDK, and supporting certificate and retry packages. The broad SDK package imports AWS, Google Cloud, and OCI authentication implementations even though LeapView needs only Universal Auth, OIDC, and one secret read.

`govulncheck` found five reachable vulnerabilities in the SDK's standalone minimum-version graph. LeapView already selects newer fixed versions of the affected shared gRPC, OpenTelemetry, and `golang.org/x` modules; the SDK probe in the resolved LeapView module graph had zero reachable vulnerabilities. Adoption would therefore not introduce a known reachable vulnerability at this revision, but it would increase dependency and update coupling.

No production code was replaced, so the measured LOC reduction is zero. A policy-preserving wrapper would still need LeapView-owned allowlist validation, transport enforcement, response bounds, context propagation, error mapping, current v4 retrieval, bundle validation, OIDC token sourcing, and deterministic token invalidation. It would add code rather than remove the expected 250–400 lines.

## Decision

Do not adopt v0.8.0. Keep the existing resolver and its negative security tests.

Re-evaluate a later SDK only when its narrow auth-and-secrets surface:

- uses the current v4 secret API;
- accepts an injected HTTP client or transport with caller-owned redirect and TLS policy;
- accepts a context on every network operation and supports an injected clock;
- enforces configurable response limits before decoding;
- supports an OIDC workload-token source for re-authentication;
- does not expose provider response values by default; and
- can be imported without linking unrelated cloud authentication stacks.

This is a versioned fit decision, not a permanent rejection of an official client.

## Confirmation

Resolver security tests must retain the allowlist, exact-origin, redirect,
response-bound, cancellation, credential-redaction, token-refresh, and retry
invariants recorded above. A future SDK evaluation either confirms all of those
properties or supersedes this ADR with new evidence.
