# API quickstart

The headless API is served beneath `/api/v1`. This guide verifies authentication, discovers a project, and shows how to move from raw HTTP to generated operation metadata.

## Before you begin

Choose a non-production or read-only project target, create a narrowly scoped credential, and install `curl` plus the LeapView CLI version compatible with the target. Keep a request-size limit and timeout policy ready for the client you will build.

Follow this discovery path:

1. Store the target and token outside source code and shell history.
2. Identify the authenticated principal.
3. List authorized projects and dashboards with bounded pagination.
4. Describe the generated operation before calling it from the CLI.
5. Validate status and schema handling, then verify an end-to-end read.

## Create a scoped credential

Use a dedicated service principal or a user token issued for this integration. Grant only the project and privileges needed for the first call. Store the values in the current shell from a secret manager:

```sh
export LEAPVIEW_TARGET=https://dash.example.com
export LEAPVIEW_API_TOKEN=<secret>
```

Do not include bearer tokens in URLs. Avoid shell tracing while secrets are present.

## Verify the principal

```sh
curl --fail-with-body \
  --silent --show-error \
  --header "Authorization: Bearer $LEAPVIEW_API_TOKEN" \
  --header "Accept: application/json" \
  "$LEAPVIEW_TARGET/api/v1/me"
```

A `200` response identifies the authenticated principal. `401` means the credential is absent, invalid, expired, or revoked. `403` on a later operation means authentication succeeded but effective privilege is insufficient.

## List projects

Request a bounded page:

```sh
curl --fail-with-body \
  --silent --show-error \
  --header "Authorization: Bearer $LEAPVIEW_API_TOKEN" \
  --header "Accept: application/json" \
  "$LEAPVIEW_TARGET/api/v1/projects?limit=50"
```

Use stable project IDs from the response in project-scoped path parameters. Titles are display metadata and are not safe identifiers. If the response provides a next-page token, pass it back as `pageToken` without inspecting or modifying it.

## Discover a dashboard

With a project ID such as `project:commerce`:

```sh
curl --fail-with-body \
  --silent --show-error \
  --header "Authorization: Bearer $LEAPVIEW_API_TOKEN" \
  --header "Accept: application/json" \
  "$LEAPVIEW_TARGET/api/v1/dashboards?limit=50"
```

The BI API then provides dashboard description, page-component discovery, filter options, coordinated page queries, visual data, and table windows. Request and response bodies are defined by OpenAPI; do not infer them from browser network traffic.

## Use the generated CLI API client

The CLI can discover and call operations from the generated API registry:

```sh
leapview api list
leapview api describe <operation>
leapview api call <operation> \
  --target "$LEAPVIEW_TARGET" \
  --token "$LEAPVIEW_API_TOKEN"
```

Use repeatable `--path key=value` and `--query key=value` arguments for parameters. Supply JSON through `--body-json` for small controlled values or `--body-file` to keep larger payloads out of shell quoting and history.

## Handle responses safely

Set client timeouts, check status before decoding success payloads, cap response sizes appropriate to the operation, and avoid automatic retries for state-changing requests unless the operation documents safe idempotency. Honor `429` backoff and preserve request/operation IDs from responses or logs for support.

## Validate the integration contract

Use the downloadable OpenAPI document as the source of request and response shape. Confirm the client distinguishes authentication, authorization, validation, rate-limit, and server errors before adding business logic. Run the same safe read with an expired or revoked credential and with a credential lacking project access; both paths must fail closed without logging the bearer token.

## Verify an end-to-end read

Select one dashboard from discovery, describe its generated operation, and request a bounded read for a known page or visual. Compare the returned project and dashboard IDs with the discovery response, verify the status and content type before decoding, and confirm a correlation identifier is retained for support. Revoke the temporary credential when the quickstart is complete.

## Troubleshooting

For `401`, check token source, expiry, revocation, and target origin. For `403`, inspect effective project privileges instead of replacing the token with an administrator credential. For `404`, use IDs from discovery rather than display titles. For `429`, honor server backoff and reduce concurrency. If decoding fails on a success response, compare the target's OpenAPI contract with the client version before changing parsing heuristics.

## Next steps

Use the generated [API reference](/docs/api), [API conventions](/docs/guides/integrate/api-conventions), and downloadable [OpenAPI document](/docs/openapi.yaml) for exact contracts. For automation, move the tested operation into a client with bounded retries, structured logging, and credential rotation.
