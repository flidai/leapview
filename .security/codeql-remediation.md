# CodeQL remediation evidence

Baseline: `ad2c9bfb6b148834268e422ff2e2bbc1670acc81`, security run
[37200497398](https://github.com/flidai/leapview/actions/runs/37200497398).
The baseline Go job reported 44 unresolved generated packages while succeeding.

Tracked in [Linear project P-FAI-65](https://linear.app/flid/project/leapview-codeql-reliability-and-security-alert-remediation-c31a24fb8dc0),
FAI-1077–FAI-1086, led by Ganesh Kambli, target 11 October 2026.
Implementation and hosted acceptance completed on 5 October 2026; all ten issues
are Done and the project is Completed.

## Alert-by-alert review

These dispositions were rechecked against the healthy main scan on
`77ecf56bec6ee4f9a4018c869c802ee545285583` on 5 October 2026.
GitHub marks alerts 1 and 52 fixed; the other five remain open. No new findings
appeared. Open alert status is distinct from completed review and remediation.
No alert is automatically dismissed. Disappearance caused by a configuration
identity change or deleted analysis history is not evidence of remediation.

| Alert | Evidence and implementation | Verified disposition |
| --- | --- | --- |
| [91](https://github.com/flidai/leapview/security/code-scanning/91) | `transport.ClientIDCookies` now derives from configured cookie security/canonical origin, with direct TLS also secure. It refreshes valid IDs before headers, without trusting forwarded protocol. Dashboard, builder, admin, project, agent, and login use that policy. Transport and application proxy tests cover issuance. | Policy inconsistency fixed; alert remains open for conditional HTTP behavior. Secure cookies passed certificate-verified HTTPS through the real pinned deployment proxy in a local fixture, not a live production deployment. Supported HTTP intentionally remains non-Secure. HTTPS deployments must retain correct canonical-origin/security configuration. Production exploitation was not established. |
| [1](https://github.com/flidai/leapview/security/code-scanning/1) | Accessibility debugger dispatch uses a private `Map` of outstanding numeric IDs. Added non-null object and positive safe-integer validation, callable retrieval, deletion before invocation, and controlled rejection/cleanup on malformed frames and socket failures. Fake-WebSocket regression covers the reproduced null exception. | GitHub marks the alert fixed; fresh JavaScript analysis has no findings. The reproduced malformed-message defect is fixed; the original Map dispatch was already protected. |
| [52](https://github.com/flidai/leapview/security/code-scanning/52) | Existing bounds prevented platform-int overflow. `decode_integer.go` now uses `strconv.Atoi` after JSON-number decoding; int64 parsing remains separate. Boundary tests execute the same production functions on amd64 and 386. | GitHub marks the alert fixed; absent from fresh Go results. Tests executed the production parser on both integer widths. |
| [49](https://github.com/flidai/leapview/security/code-scanning/49) | Both `QuerySemanticModel` and `PreviewSemanticDataset` normalize <=0 to 100 and clamp >1000 to 1000, request one extra row, then allocate/return at most the normalized limit. HTTP caller tests assert physical limits, returned counts, and pagination for omitted/negative/max-int/oversized/malformed inputs. | Alert remains open on the same bounded response allocation: reviewed as a narrow false positive. This does not establish bounds on every query-memory path. |
| [47](https://github.com/flidai/leapview/security/code-scanning/47) | `PrincipalIdentityManagement.HasLocalPassword` is a boolean capability input to `currentPrincipalResponseFor` → `canChangePassword` → `resourceETag`. SHA-256 hashes the JSON representation, not a credential. Response-field allowlist and ETag stability/change tests retain the public contract. Password-verifier tests continue using Argon2id. | Alert remains open: false positive on this reviewed source-to-sink path. Keep deterministic metadata hashing. |
| [48](https://github.com/flidai/leapview/security/code-scanning/48) | `HasLocalPassword` → `principalAdministrationDTO` → `canResetPassword` reaches generic `apiItemPageKey`. Principal objects have an ID and use the created-at/ID key; generic metadata fallback SHA-256 also contains capability data, not password material. Tests cover response fields and key stability. | Alert remains open: false positive on this reviewed source-to-sink path. |
| [87](https://github.com/flidai/leapview/security/code-scanning/87) | Local sign-in allows only `http://127.0.0.1:<port>`, disables auth redirects, binds loopback, and hands off over a random two-minute endpoint. Tests reject remote/lookalike/userinfo/path/query/fragment inputs before browser opening and reject wrong paths/methods without cookies. | Alert remains open: intentional local HTTP behavior, documented next to code. Cookies are not port-scoped; hostile local processes are outside this isolation boundary. Secure does not solve that boundary. |

## Local verification and reproducible commands

- `task security:policy` covers inventory, workflow sequencing/failure paths, module command failures, dependency drift (including a newly created ignored sum/lock), and raw-SARIF health. Findings without extraction diagnostics are allowed by the health checker.
- `task security:sast:prepare` runs generation sequentially in each job. Clean TypeScript test imports also require `visual-docs:generate`, discovered during implementation.
- `task security:sast:typescript` checks app, contracts, tests, playground, APIGen, and desktop source graphs without release bundles.
- `go test ./pkg/duckdbsql` covers decoder behavior and malformed-error classification. `GOARCH=386 CGO_ENABLED=0 go test ./pkg/duckdbsql/decode_integer.go ./pkg/duckdbsql/decode_integer_test.go` executes the same production parsing code at 32 bits, avoiding DuckDB's unsupported 386 native bindings. This is execution, not cross-compilation alone.
- `task desktop:test` includes malformed protocol regression tests and the desktop build. Packaged Linux verification uses the existing preview workflow's root-owned mode-4755 sandbox helper and Xvfb, preserving Chromium sandbox enforcement.
- The locked actionlint version does not recognize the existing `pull_request: stacked` trigger. Validation with that single existing activity-type diagnostic ignored passes; triggers remain unchanged.

## Completed hosted acceptance

| Proof | Candidate and result |
| --- | --- |
| Reviewed PR | Head `e78f96d4b59cc9b3792aff55ae3d7dc22e35aebc`, tested merge `70036c94b63171eaccaf98fead8f4e779981d79c`. [Security](https://github.com/flidai/leapview/actions/runs/37268797368), [PR CI](https://github.com/flidai/leapview/actions/runs/37268797401), [Nix development / complete task ci](https://github.com/flidai/leapview/actions/runs/37268797334), [deployment scaffold](https://github.com/flidai/leapview/actions/runs/37268797335), and [Electron proof](https://github.com/flidai/leapview/actions/runs/37268797252) passed. |
| Merge queue | Actual merged candidate `6682845926675dc615515c90a31a14690f24f987`: [full CI](https://github.com/flidai/leapview/actions/runs/37271505086), [security](https://github.com/flidai/leapview/actions/runs/37271504726), and [Electron](https://github.com/flidai/leapview/actions/runs/37271504698) passed. |
| Same-commit cold/warm | [Cold](https://github.com/flidai/leapview/actions/runs/37271504726) and [warm](https://github.com/flidai/leapview/actions/runs/37274120579) used `6682845926675dc615515c90a31a14690f24f987`. Both gates passed; exact successful extraction sets matched: 1,900 Go files and 816 JavaScript files. |
| Missing generated source | [37272405201](https://github.com/flidai/leapview/actions/runs/37272405201): removing 18 generated Go files failed read-only package discovery, Go SAST, and Security gate; JavaScript passed and its raw report was retained. |
| Diagnostic warning | [37272408056](https://github.com/flidai/leapview/actions/runs/37272408056): injected raw-SARIF warning failed JavaScript health and Security gate after successful analysis; diagnostic artifact retained. Other lanes passed. |
| Missing SARIF | [37272410720](https://github.com/flidai/leapview/actions/runs/37272410720): withholding the expected JavaScript filename failed health and Security gate; the report under its withheld filename remained downloadable. Other lanes passed. |
| Current main at reassessment | `77ecf56bec6ee4f9a4018c869c802ee545285583`, including the subsequent host-upgrade change: [security](https://github.com/flidai/leapview/actions/runs/37274174285), [Electron](https://github.com/flidai/leapview/actions/runs/37274174189), [main image qualification](https://github.com/flidai/leapview/actions/runs/37274174656), and [public site image](https://github.com/flidai/leapview/actions/runs/37274174527) passed. |

Cold/warm runs used Go 1.27.1, Node 24.20.0, Bun 1.3.14, and CodeQL CLI 2.27.1.
Cold Go/JavaScript job durations were 10m58s/7m43s; warm durations were
11m08s/4m44s. Both warm jobs restored the real 1,009,340,064-byte shared Go cache
(cache ID 8504235157). Forced `go build -a` still compiled all four maintained
modules: root, `deploy/kamal-trial`, `pkg/apigen`, and `pkg/apigen/example`.
This proves extraction despite cache restoration, not a warm Go speedup.
Pre/post dependency integrity and raw-SARIF health checks passed.

All three negative proofs used disposable candidate
`37d090465f962fb8fce00b907ae1c85b8d140406`. A changed TypeSpec input regenerated
into both Go and TypeScript before extraction in both matrix jobs. Synthetic
analysis uploads were disabled; fault switches were never merged. Earlier
harness attempts failed because `rg` was absent and are not counted as proof.

Main Go analysis `1891383157` and JavaScript analysis `1891370592` have empty
error/warning fields. Their raw reports contain successful invocations and no
warning/error diagnostics; Go has 1,901 successful file extractions and five
reviewed findings, JavaScript 816 and zero. The first main Go analysis at
`66828459` was cancelled by the subsequent push and is not counted as a success.
Historical reports remain preserved.

Raw reports use fourteen-day artifact retention. Durable run references,
artifact IDs, SHA-256 checksums, command/log evidence, and individual alert traces
are recorded in [FAI-1085](https://linear.app/flid/issue/FAI-1085) and
[FAI-1086](https://linear.app/flid/issue/FAI-1086).

## Verification limits and separate policy work

Focused security/policy tests, executed amd64/386 parsing boundaries, desktop
build/tests, sandboxed packaged accessibility, and the local real-proxy HTTPS
fixture passed. Full local `task ci` did not complete: earlier attempts hit the
browser-shard watchdog, and the reviewed-head attempt was blocked by the shared
host's missing Docker bridge. Complete hosted `task ci` passed; no local full-CI
pass is claimed. The existing unsupported `stacked` actionlint diagnostic is
still a tooling limitation, not a trigger change.

The required Security gate enforces analysis health, not vulnerability severity.
A separate ruleset/result-gate proposal remains documented in
[the plan](../plan.md#5-record-the-separate-merge-policy-decision). Native CodeQL
merge protection has PR-diff and merge-queue limitations. This project did not
change repository settings or introduce severity enforcement.

The five open alerts have reviewed dispositions, not GitHub dismissals. Any
later dismissal should record its individual evidence: false-positive rationale
for alerts 47/48/49, and accepted HTTP behavior/trust boundaries for 87/91.
No blanket query exclusion, analysis-history deletion, or automatic dismissal
was used to obtain completion.
