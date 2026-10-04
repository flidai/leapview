# CodeQL remediation evidence

Baseline: `ad2c9bfb6b148834268e422ff2e2bbc1670acc81`, security run
[37200497398](https://github.com/flidai/leapview/actions/runs/37200497398).
The baseline Go job reported 44 unresolved generated packages while succeeding.

Tracked in [Linear project P-FAI-65](https://linear.app/flid/project/leapview-codeql-reliability-and-security-alert-remediation-c31a24fb8dc0),
FAI-1077–FAI-1086, led by Ganesh Kambli, target 11 October 2026.

## Alert-by-alert review

These are reviewed implementation dispositions, pending a complete hosted rescan.
No alert is automatically dismissed. Disappearance caused by a configuration
identity change or deleted analysis history is not evidence of remediation.

| Alert | Evidence and implementation | Disposition after healthy rescan |
| --- | --- | --- |
| [91](https://github.com/flidai/leapview/security/code-scanning/91) | `transport.ClientIDCookies` now derives from configured cookie security/canonical origin, with direct TLS also secure. It refreshes valid IDs before headers, without trusting forwarded protocol. Dashboard, builder, admin, project, agent, and login use that policy. Transport and application proxy tests cover issuance. | Fixed policy inconsistency; verify deployed HTTPS response and alert state. Production exploitation was not established. |
| [1](https://github.com/flidai/leapview/security/code-scanning/1) | Accessibility debugger dispatch uses a private `Map` of outstanding numeric IDs. Added non-null object and positive safe-integer validation, callable retrieval, deletion before invocation, and controlled rejection/cleanup on malformed frames and socket failures. Fake-WebSocket regression covers the reproduced null exception. | Adjacent malformed-message defect fixed. If the dispatch finding persists, review as false positive on the protected Map path; do not equate the two issues. |
| [52](https://github.com/flidai/leapview/security/code-scanning/52) | Existing bounds prevented platform-int overflow. `decode_integer.go` now uses `strconv.Atoi` after JSON-number decoding; int64 parsing remains separate. Boundary tests execute the same production functions on amd64 and 386. | Simplification of a bounded path; verify resulting scan. |
| [49](https://github.com/flidai/leapview/security/code-scanning/49) | Both `QuerySemanticModel` and `PreviewSemanticDataset` normalize <=0 to 100 and clamp >1000 to 1000, request one extra row, then allocate/return at most the normalized limit. HTTP caller tests assert physical limits, returned counts, and pagination for omitted/negative/max-int/oversized/malformed inputs. | Narrow false-positive disposition if the same bounded response allocation persists. This does not establish bounds on every query-memory path. |
| [47](https://github.com/flidai/leapview/security/code-scanning/47) | `PrincipalIdentityManagement.HasLocalPassword` is a boolean capability input to `currentPrincipalResponseFor` → `canChangePassword` → `resourceETag`. SHA-256 hashes the JSON representation, not a credential. Response-field allowlist and ETag stability/change tests retain the public contract. Password-verifier tests continue using Argon2id. | False positive on this source-to-sink path if still reported. Keep deterministic metadata hashing. |
| [48](https://github.com/flidai/leapview/security/code-scanning/48) | `HasLocalPassword` → `principalAdministrationDTO` → `canResetPassword` reaches generic `apiItemPageKey`. Principal objects have an ID and use the created-at/ID key; generic metadata fallback SHA-256 also contains capability data, not password material. Tests cover response fields and key stability. | False positive on this source-to-sink path if still reported. |
| [87](https://github.com/flidai/leapview/security/code-scanning/87) | Local sign-in allows only `http://127.0.0.1:<port>`, disables auth redirects, binds loopback, and hands off over a random two-minute endpoint. Tests reject remote/lookalike/userinfo/path/query/fragment inputs before browser opening and reject wrong paths/methods without cookies. | Intentional local HTTP behavior, documented next to code. Cookies are not port-scoped; hostile local processes are outside this isolation boundary. Secure does not solve that boundary. |

## Local verification and reproducible commands

- `task security:policy` covers inventory, workflow sequencing/failure paths, module command failures, dependency drift (including a newly created ignored sum/lock), and raw-SARIF health. Findings without extraction diagnostics are allowed by the health checker.
- `task security:sast:prepare` runs generation sequentially in each job. Clean TypeScript test imports also require `visual-docs:generate`, discovered during implementation.
- `task security:sast:typescript` checks app, contracts, tests, playground, APIGen, and desktop source graphs without release bundles.
- `go test ./pkg/duckdbsql` covers decoder behavior and malformed-error classification. `GOARCH=386 CGO_ENABLED=0 go test ./pkg/duckdbsql/decode_integer.go ./pkg/duckdbsql/decode_integer_test.go` executes the same production parsing code at 32 bits, avoiding DuckDB's unsupported 386 native bindings. This is execution, not cross-compilation alone.
- `task desktop:test` includes malformed protocol regression tests and the desktop build. Packaged Linux verification uses the existing preview workflow's root-owned mode-4755 sandbox helper and Xvfb, preserving Chromium sandbox enforcement.
- The locked actionlint version does not recognize the existing `pull_request: stacked` trigger. Validation with that single existing activity-type diagnostic ignored passes; triggers remain unchanged.

## Hosted acceptance still required

Record candidate SHA, toolchain versions, cold/warm durations, all four traced
modules, and both raw SARIF files. Confirm generated import warnings are absent,
warm caches do not suppress compilation, and dependency files remain unchanged.
Use disposable candidates to remove generated source, inject a diagnostic
warning, remove SARIF, and change TypeSpec input. Verify regeneration/failure
propagation to Security gate and artifact availability. Keep the candidate's
production workflow free of test-only failure switches.

Review every original and newly surfaced finding against the new analysis.
Record actual PR, merge-group, and main commits separately. Keep hosted acceptance
and alert closure open in Linear until this evidence exists. Do not change branch
protection, blanket-disable queries, delete analysis history, or auto-dismiss alerts.
