# Credential lifecycle: current-state inventory

Status: research only; Step 1 input to a future credential-lifecycle ADR

Research date: 2026-09-27

Application baseline: `8283839939dd630e2a1874927c62fdb8ccadfa8d`

Proposal baseline: [PR #744](https://github.com/flidai/leapview/pull/744), draft
head `ac6b0986d52771776535b74e250774a94aecbe91` (checked on the research date).

## Purpose and limits

Inventory existing secret inputs, owners, persistence, consumers, update paths
and recovery dependencies before designing the shared credential lifecycle.
This document records source-code observations and explicit unknowns. It does
not accept the draft deployment proposal, specify a new activation protocol,
claim production qualification, or report a demonstrated vulnerability.

The research examined source, configuration declarations, deployment templates,
accepted ADRs and existing tests. It did not inspect deployed secret values,
private environment files, live databases or customer infrastructure. Existing
tests cited below were inspected, not executed. The local reference-library
lookup returned no matching source for this topic.

The supplied three-page “Proposed target stack” PDF agrees with the PR on
dedicated managed application/database VPSs, the same credential implementation
for self-hosted and managed installations, separate recoverable keys, and
accepted maintenance downtime. It does not define activation success. The
pinned PR is the repository-accessible source for those proposed choices.

## Classification and proposed destination

Classification follows ownership and purpose, not the current input mechanism.
For example, supplying a customer provider key through an environment variable
does not make its purpose a deployment credential. A database password for
LeapView's own control database differs from one for a customer's data source.

| Class | Owner/purpose | Proposed destination in PR #744 |
|---|---|---|
| Deployment/operations | Operator provisioning, deployment, maintenance and backup authority | Scoped GitHub environment secrets or short-lived identity; delivered only to the relevant job/host |
| Application bootstrap | Inputs needed to start the application and reach its state; includes encryption/signing keys | Protected operator-supplied host configuration; essential keys independently recoverable |
| Customer/domain | Recoverable external credentials used for customer integrations | Application-encrypted PostgreSQL records through one authorized, audited UI/API/bootstrap service |

LeapView-issued authentication credentials are a separate lifecycle boundary,
not a proposed fourth secret category. Verification-only server records must
keep their existing hashing semantics. A client may still need to retain the
raw bearer credential to authenticate. The client OS keychain is also distinct
from the proposed server-side deployment encryption keyring.

Source: [proposal secrets ownership and lifecycle](https://github.com/flidai/leapview/blob/ac6b0986d52771776535b74e250774a94aecbe91/adr/0025-share-an-open-deployment-stack-for-self-hosted-and-managed-leapview.md#L327-L382).

## Customer/provider credential flows

| ID / material | Current input and persisted authority | Consumers and scope | Current change behavior / retirement dependency | Proposed destination |
|---|---|---|---|---|
| D1: external connection credential bundle | Target binding stores a scoped credential reference, endpoint and validation evidence, not the bundle value. Production resolver selection supports Infisical; environment resolution is development-only. | Target/project/environment binding; connector pools and their leases. | When refresh/test/acquisition invokes refresh, the manager can prepare and health-check a candidate, save binding evidence with expected revision, then switch its local pool. This is distinct from publishing a new serving generation; prior leases and generation pins can remain. | Shared domain service plus an explicitly selected PostgreSQL resolver; retain optional Infisical behavior when selected. |
| D2: development connection bundle | Explicitly selected `LEAPVIEW_DEV_CONNECTION_*` variables and profile configuration. | Development target only; selected variable allowlist and bounded snapshot. | Refresh observes the selected input. This is not a production credential-write service. | Preserve the explicitly scoped development path; define its relationship to the new service in Step 2. |
| D3: deployment-supplied agent provider key | `LEAPVIEW_AGENT_API_KEY` or deployment-mounted JSON selected by `LEAPVIEW_AGENT_CONFIG_FILE`; runtime config initially owns the value. | Instance agent runtime and requests using a captured runtime. | File reload is an existing path. Administrator-managed saved configuration has takeover rules; this cannot be modeled as two interchangeable authorities. | Classify by actual provider-account owner; customer-owned keys belong to domain storage. Decide compatibility for platform-funded/deployment-owned provider accounts. |
| D4: administrator-managed agent provider key | Shared UI/API request enters `ConfigurationManager`; immutable PostgreSQL configuration revisions store ciphertext and non-secret config. The record has no project/environment owner. | Instance administration, current agent runtime and runs pinned to historical configuration revisions. | Test proof binds actor, expected revision and input/config. Save inserts a new revision then installs local runtime. Resume can decrypt a previous revision. | Account ownership remains unresolved, as for D3: customer-owned provider credentials fit domain storage; platform-owned credentials retain bootstrap/deployment ownership. UI storage alone does not decide ownership. Preserve or explicitly transition existing history in either case. |
| D5: workload identity / public connection | Binding validation accepts a workload-identity mode or no authentication, without a credential reference. | The inspected DuckDB target-binding adapter supports external bundles and no-auth; it rejects the workload mode. | A declared mode is not proof of an implemented provider identity flow. No-auth requires no reusable secret. | Preserve no-auth; treat workload-provider support as an explicit separate boundary. |

Evidence:

- [Binding identities, authentication modes and credential references](../../internal/analytics/connectionbinding/binding.go#L58), [resolver selection](../../internal/analytics/connectionbinding/resolver.go#L11), [target resolver composition](../../internal/analytics/module/connection_credentials.go#L22).
- [Binding persistence](../../internal/analytics/connectionbinding/postgres/repository.go#L47), [pool refresh](../../internal/analytics/connectionbinding/rotation.go#L184), [bounded retirement](../../internal/analytics/connectionbinding/rotation.go#L585).
- [Agent input and test/save implementation](../../internal/agent/configuration.go#L40), [agent persistence](../../internal/agent/postgres/configuration.go#L43), [immutable ciphertext schema](../../internal/platform/postgres/migrations/029_agent_configuration.sql#L4), [deployment file loading](../../internal/agent/configreload/file.go#L24).
- [Connector credential fields](../../internal/analytics/connectors/registry.go#L85), [supported runtime authentication modes](../../internal/analytics/duckdb/binding_credentials.go#L10), [private local profile inputs](../../internal/app/cli/localruntime/controller.go#L230).

The current production contract is stronger than simply resolving the latest
provider value: publication pins a provider version to each serving generation.
The documented update sequence is validate the new binding version, create a
new candidate and publish it. Existing generations retain their pins; restart
and rollback require the historical provider version to remain available.
The inventory does not assume that a new shared credential service may silently
override this contract. Sources: [production connection lifecycle](../../docs/articles/operate/production-configuration.md#L43),
[release evidence](../../internal/release/provenance.go#L48),
[exact-version serving resolution](../../internal/analytics/module/active_runtime_bindings.go#L90).

Disabling a LeapView binding does not delete/revoke the external provider
secret, and replacing a password does not necessarily terminate source-side
sessions. Immediate upstream revocation/session termination remains a separate
provider action. Sources: [binding disable](../../internal/analytics/connectionbinding/rotation.go#L465),
[documented source-session boundary](../../docs/articles/operate/production-configuration.md#L58).

## Bootstrap and operations flows

| ID / material | Current input/storage | Consumer and privilege boundary | Update/recovery observation | Proposed classification |
|---|---|---|---|---|
| B1: control and DuckLake runtime PostgreSQL URLs | Explicit secret-bearing configuration URLs. | Runtime PostgreSQL clients and per-connector DuckDB credential bootstrap. They are distinct from customer source passwords. | Host/application configuration must reconnect to the restored database. No shared domain credential service owns these inputs today. | Bootstrap |
| O1: PostgreSQL migrator, maintenance and upgrade-coordinator URLs | Separately declared configuration inputs with operation-specific scopes. | Initialization, upgrade and bounded maintenance identities; inspect the actual operation rather than infer privilege from the variable name. | Recovery and upgrades need the appropriate separate identity, not wholesale delivery of all roles to serving. | Operations |
| B2: agent encryption key | `LEAPVIEW_AGENT_CREDENTIAL_KEY`; one 32-byte hex key supplied to the configuration manager, also used for its validation-proof HMAC. | Encrypts/decrypts all persisted agent credential revisions in this implementation. | Existing keys are preserved by initialization; missing keys can be generated by the bootstrap helpers. Those helpers alone do not establish the proposed encrypted-state-aware missing-key recovery rule. | Bootstrap; predecessor of the proposed versioned keyring |
| B3: Infisical machine authentication | Universal Auth client ID/secret, HTTPS origin and exact allowed scopes in target configuration. An OIDC authenticator also exists as an adapter. | Narrow external credential resolver; machine auth differs from the domain secret it retrieves. | Requires its selected provider's availability/authentication lifecycle. Presence of an adapter does not prove every deployment wires it. | Bootstrap for explicitly selected Infisical |
| B4: managed-data / physical-pool S3 credentials | `LEAPVIEW_MANAGED_DATA_S3_*` configuration or supported ambient provider credentials. | Managed-data storage, admitted physical-pool bootstrap and cleanup consumers. | Storage credential retirement depends on these consumers, not only user-facing connection pools. | Bootstrap for LeapView-owned storage; ownership must be reassessed for a customer-source integration |
| B5: immutable object-store credentials and SSE-C key | `LEAPVIEW_OBJECT_STORE_S3_*`; access credentials and the raw SSE-C key are distinct from opaque encryption references/provider key IDs. | Immutable authored sources/artifacts and object-store consumers. | Restoring ciphertext/objects requires the corresponding external encryption material. Database application encryption and storage encryption are separate. | Bootstrap |
| O2: provider-restore secret bundle | Plaintext host JSON containing provider PostgreSQL URLs, object credentials and trust roots, represented elsewhere by a digest/reference. | Host/provider restore tooling; path ownership and effective permissions depend on the caller/host setup. | Save requests directory/file creation modes 0700/0600 but does not repair existing modes; Load checks digest/schema, not owner/mode. The digest is content identity, not encryption or independent authenticity. The bundle does not contain the agent keyring. | Operations/recovery; not proof of independent encrypted application-key recovery |
| O3: CI provisioning, registry and deployment credentials | Current demo and Hetzner workflows use GitHub OIDC with a pinned Infisical action; registry access also uses scoped GitHub tokens. Hetzner qualification generates a runner SSH key. | Build, provisioning and deployment jobs/hosts. Demo runtime deployment has a manual/main/environment gate. | Current OIDC-to-Infisical delivery is distinct from the proposed GitHub-secrets/Kamal delivery. Actual configured secret values/permissions were not inspected. | Operations |
| O4: workload exchange client secrets | Demo publishing uses separate publisher/release identities and mints scoped workload tokens. | CLI publishing/release operations; this script is not application-host deployment. | Client secrets are unset after exchange; token and service-principal revocation remain access-domain responsibilities. | Operations/client authentication |
| O5: migration evidence signing key | Private signing key belongs to the subsystem owner; runtime consumes a trusted public-key registry and signed evidence. | Release/migration authorization verification. | Historical public keys may remain for immutable evidence verification. Private signer custody is outside the evidence contract. | Operations; not a domain encryption keyring |

Evidence:

- [Configuration catalogue](../../internal/app/config/spec/spec.go#L121), [runtime DuckLake bootstrap](../../internal/app/postgres_credentials.go#L22), [ephemeral connector credential adapter](../../internal/app/postgresducklake/credentials.go#L39).
- [Compose initialization](../../internal/app/cli/composectl/controller.go#L631), [host candidate key handling](../../internal/app/cli/hostinstall/maintenance_native_effects_linux.go#L382), [agent encryption/decryption](../../internal/agent/configuration.go#L62).
- [Provider restore bundle](../../internal/app/providerrestore/secret_bundle.go#L20), [current Hetzner deployment workflow](../../.github/workflows/hetzner-deploy.yml#L61).
- [Demo workflow identity delivery](../../.github/workflows/demo-deploy.yml#L103), [runtime deployment gate](../../.github/workflows/demo-deploy.yml#L140), [workload exchange](../../scripts/deploy_demo.sh#L25), [owner signing/verification boundary](../../internal/release/migrationcapability/owner_evidence.go#L32).

Compose supplies application configuration through an `env_file`; current host
documentation places that file at `/opt/leapview/leapview.env`. It is separate
from the application-state volume. Host snapshots capture the configured
stopped-directory set, not automatically every host secret. Provider-native
backups and retained keys are separate operational dependencies. Sources:
[Compose mounts](../../deploy/compose/compose.yaml#L14),
[host paths and backup guidance](../../deploy/hetzner/README.md#L114),
[snapshot primitive](../../internal/app/cli/hostinstall/maintenance_snapshot_unix.go#L39).

There is already an application-startup guard for saved administrator agent
configuration without a supplied key, and decryption fails for a wrong key.
This must be distinguished from bootstrap helpers that generate an absent key
without inspecting the database. Source:
[startup guard](../../internal/agent/module/module.go#L223).

## Authentication and other adjacent secret boundaries

These inputs are inventoried to prevent accidental migration into the external
credential store. They are not a request to redesign authentication.

| ID / material | Existing responsibility | Classification / Step 2 boundary |
|---|---|---|
| A1: local passwords | Argon2id verifiers in PostgreSQL; admin reset/create can return a temporary password for the administrator to distribute. | Preserve verification-only storage and existing reset semantics. |
| A3: personal API tokens, browser/desktop sessions and service-principal secrets | Random bearer material returned to its client; server lookup uses a keyed fingerprint plus an Argon2id verifier and lifecycle metadata. | Preserve hashing, exact authority and revocation. These server records do not contain an encrypted bearer value to recover. |
| A4: authoring device/access/refresh tokens | Server records use SHA-256 token hashes and expiry/revocation/rotation state. | Preserve this existing credential-specific verification contract; do not assume every access credential uses the same hashing scheme. |
| B6: CSRF, MCP signing and token-fingerprint keys | `LEAPVIEW_CSRF_KEY` protects CSRF/OAuth state and is a source for local MCP signing material; `LEAPVIEW_TOKEN_HASH_KEY` falls back to the CSRF key. | Bootstrap. Fingerprint-key rotation affects lookup of existing stored bearers; provider-credential rotation does not solve it. External MCP issuer verification is a separate supported configuration. |
| B7: OIDC / Azure client secrets | Deployment-configured login integration credentials. | Bootstrap in the inspected configuration; a future customer-managed integration needs an explicit ownership decision. |
| B8: metrics and SCIM bearer tokens | Deployment-configured credentials protecting incoming endpoints. | Bootstrap/operations inputs for verification; not automatically encrypted customer provider records. |
| A2: CLI/desktop credentials and workload exchange inputs | Native OS credential storage, ephemeral CLI input or workload identity exchange. | Client credentials and operations identity. OS keychain storage is not the deployment keyring or its backup. |
| P1: platform email and customer email-provider keys | The proposal identifies platform email as bootstrap and a customer-owned provider key as domain data. | SMTP/Postmark implementation was not located in the inspected Go/deployment/workflow sources. Record the proposed boundary without inventing an existing lifecycle. |

Evidence: [secret configuration declarations](../../internal/app/config/spec/spec.go#L60),
[native client secret store](../../internal/platform/securestore/securestore.go#L1),
[accepted typed-credential contract](../0025-adopt-typed-resource-permissions-and-scoped-api-credentials.md),
[accepted authority-flow contract](../0026-preserve-authority-across-governed-operations.md).

Credential-specific evidence: [password/bearer verification](../../internal/access/postgres/access_core.go#L90),
[personal-token persistence](../../internal/access/postgres/scoped_tokens.go#L42),
[service-principal secret persistence](../../internal/access/postgres/access_core.go#L1226),
[authoring-token hashing](../../internal/access/authoring_auth.go#L440),
[fingerprint-key composition](../../internal/app/postgres_build.go#L300),
[local MCP key composition](../../internal/app/postgres_build.go#L359),
[OIDC code exchange](../../internal/access/oidc/client.go#L40),
[SCIM bearer verification](../../internal/access/scimprov/server.go#L88).

## Consumers that matter for retirement

| Consumer | Existing version/reference behavior | What a future completion claim must account for |
|---|---|---|
| Mutable connection pool manager | One process-local active generation plus draining generations and leases. | Every relevant manager and existing lease, including interrupted refresh/retirement. |
| Serving-generation connection resolver | Resolves the exact credential provider version recorded in serving evidence, checks endpoint evidence and prepares an isolated pool. | Updating a mutable binding alone does not rewrite retained serving evidence. Establish which active/retained generations can still request the old version. |
| Development profile application and candidate build | Profile application retains provider-version evidence; candidate planning/build checks non-secret binding evidence for drift. | Interrupted application/build can retain a version dependency even before publication. |
| Agent new requests | Refreshes saved configuration before starting new prompts and captures runtime/configuration revision. | Each running process and already admitted request; a saved row is not a global acknowledgment. |
| Agent resumed work | Reads `configuration_revision` from durable run metadata and can reconstruct the historical runtime. | Future resume is old-version usage even if all currently open connections have drained. |
| Agent title generation | Captures the current process-local runtime and makes a separate provider model call. | Include auxiliary/background model calls, not only main prompt runs, in drain/cancellation scope. |
| Background jobs / workers | App composition registers agent, refresh, release, deployment and optional upload handlers with River. The agent handler resumes durable prompts. | Source proves in-process composition, not actual deployment replica count. Establish worker placement and overlap; a VPS count does not establish singleton execution or authorize a new job kind. |
| Bootstrap storage/DB consumers | Use configuration-owned credentials outside the customer connection manager. | Their rotation belongs to the relevant bootstrap/operations contract. |
| Historical backup / supported rollback | Old database state can contain old ciphertext, provider versions and unfinished operations. | Key retention, external provider validity and fresh runtime evidence after restore. |

Evidence: [pinned serving resolution](../../internal/analytics/module/active_runtime_bindings.go#L90),
[profile application](../../internal/analytics/connectionbinding/profile_application_service.go#L240),
[candidate binding check](../../internal/app/deploymentpostgres/native_build.go#L430),
[agent resume](../../internal/agent/prompt.go#L416),
[auxiliary model call](../../internal/agent/title.go#L35),
[job composition](../../internal/app/runtime_router.go#L969),
[River worker registration](../../internal/platform/jobs/module/module.go#L129),
[agent worker resume](../../internal/agent/module/jobs.go#L75),
[agent startup composition](../../internal/agent/module/module.go#L211),
[asynchronous authority boundary](../0026-preserve-authority-across-governed-operations.md).

## Existing authorization, audit and validation differences

1. Connection administration's `Test` calls the rotation/refresh path and can
   promote a validated replacement. It is not currently a validation-only API.
   See [the explicit method contract](../../internal/analytics/connectionbinding/administration.go#L317).
2. Agent `Test` returns a short-lived proof; `Save` validates it, persists an
   immutable revision, then installs the process-local runtime. This is useful
   precedent, not a cross-process activation-completion protocol. See
   [test/save](../../internal/agent/configuration.go#L154).
3. Generated connection-management operations use scoped `connection.manage`;
   agent configuration uses instance platform-settings authority. Neither
   should be flattened into a new generic administrator bypass. See
   [connection API](../../api/typespec/connection_bindings.tsp#L229) and
   [agent API](../../api/typespec/agent.tsp#L187).
   The inner administration authorizer maps its test permission to
   `connection.use`, metadata changes to `connection.manage`, and health to
   `connection.read`. Generated route and service-level checks are separate
   layers; inventory both rather than assume they are identical. See
   [composition mapping](../../internal/app/runtime_router.go#L1031).
4. Binding mutations have transactional audit, credential refresh has
   best-effort audit, and agent configuration auditing is best-effort. The new
   service's guarantees must be decided per transition. See the existing
   [audit inventory](durable-audit-inventory.json#L125).
5. Agent ciphertext history rejects updates/deletes and currently has no key-ID
   column. Re-encryption cannot be specified as an ordinary in-place update
   without explicitly addressing that history contract. See
   [migration 029](../../internal/platform/postgres/migrations/029_agent_configuration.sql#L4).
6. The agent UI submits a password-field value and reads configured-state
   metadata, not saved secret values. Its command-audit input uses the fixed
   description `provider-configuration`, and success audit identifies the
   candidate/revision. Sources: [provider settings UI](../../web/components/admin/agent-provider-settings.ts#L38),
   [UI command boundary](../../internal/agent/http/handler.go#L665),
   [test/save responses and audit](../../internal/agent/http/configuration.go#L78).
7. `PoolManager.Run` implements scheduled refresh with backoff, but this review
   did not locate a production call site for that runner. Explicit test/refresh
   and pool acquisition paths exist. Do not promise periodic production rotation
   merely because the runner and tests exist. See
   [runner](../../internal/analytics/connectionbinding/rotation.go#L152).

## Questions to settle in Step 2

| Question | Evidence that makes it necessary | Required design output |
|---|---|---|
| What counts as complete? | Database evidence, process-local pools and resumed historical work are different authorities. | Exact success/readiness/revocation conditions; saved/validated/in-use distinctions; interruption matrix. |
| Does credential replacement require publication? | The current production contract pins provider versions in serving generations and documents a new candidate/publication for a new version. | Preserve that contract or explicitly propose its amendment; distinguish binding validation from serving-generation activation. |
| What exactly did validation authorize? | Connection testing activates today; agent testing binds a proof to inputs/revision. | Non-ambiguous version/destination/configuration binding, expiry, concurrency and authorization rules. |
| Which consumers must retire? | Leases, serving-generation pins, agent historical runs and workers can outlive an update. | Consumer/deployment boundary, future old-version admission policy and positive retirement evidence. |
| Live switch or bounded interruption? | Proposed v1 permits downtime; inspected code has process-local state. | Qualified v1 cutover policy; unknown/partitioned consumers cannot be treated as stopped. |
| How are keys introduced, rotated and recovered? | One existing agent key, missing-key generation helpers and immutable ciphertext history. | Versioned keyring format/custody, fresh-install versus restore rules, resumable rotation and migration compatibility. |
| Who owns the instance agent provider account? | Both deployment inputs and administrator-saved revisions configure one instance runtime; saved records have no project/environment ownership field. | Explicit platform-owned versus customer-owned classification and migration scope; input channel cannot decide ownership. |
| When can an old key or credential be retired? | Decryptability, upstream credential validity, historical execution and backup retention are separate dependencies. | Separate retirement policies for external credentials and encryption keys. |
| Who authorizes queued lifecycle work? | Existing async authority support is scoped to qualified operations. | Caller/delegated authority, revalidation and revocation behavior for any new durable job. |
| What audit guarantee accompanies each transition? | Current paths intentionally have different guarantees. | Transactional versus best-effort decisions with redacted event contracts. |
| What does restore trust? | Old progress/version records can be restored into a new runtime. | Fresh runtime validation and fencing; old acknowledgment records alone cannot prove current completion. |
| How does optional Infisical coexist? | Current production source resolution depends on that explicit resolver. | Explicit authority selection, migration/import policy and no silent fallback on provider failure. |

The proposed deployment ADR's `0025` identifier collides with the accepted
permissions ADR in this baseline, and `0026` is also occupied. Resolve numbering
when drafting; this research document does not allocate an ADR ID or change an
accepted record.

## Coverage and verification

The inventory covers the source-level credential families above and records
unknown operational deployment details. It is not an exhaustive scan of live
secrets or an assertion that every proposed integration already exists.

### Configuration catalogue cross-check

All 41 declarations marked `Secret: true` in the baseline
[configuration catalogue](../../internal/app/config/spec/spec.go) are accounted
for below. This is a bounded coverage check, not a claim that every secret
originates in this catalogue: database-created bearers, form inputs, dynamic
development variables, native keychains and CI identities are covered separately.
Only names are recorded, never configured values.

| Inventory family | Catalogue names |
|---|---|
| D3 / B2: agent | `LEAPVIEW_AGENT_API_KEY`, `LEAPVIEW_AGENT_CREDENTIAL_KEY` |
| A2 / O4: client authentication | `LEAPVIEW_API_TOKEN`, `LEAPVIEW_WORKLOAD_CLIENT_SECRET` |
| B6: security keys | `LEAPVIEW_CSRF_KEY`, `LEAPVIEW_TOKEN_HASH_KEY` |
| B7: identity-provider authentication | `LEAPVIEW_AZURE_CLIENT_SECRET`, `LEAPVIEW_OIDC_CLIENT_SECRET` |
| B8: incoming endpoint credentials | `LEAPVIEW_METRICS_BEARER_TOKEN`, `LEAPVIEW_SCIM_BEARER_TOKEN` |
| B1: runtime database access | `LEAPVIEW_POSTGRES_CONTROL_URL`, `LEAPVIEW_POSTGRES_CONTROL_READONLY_URL`, `LEAPVIEW_POSTGRES_DUCKLAKE_URL` |
| O1: privileged database operations | `LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL`, `LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL`, `LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL`, `LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL`, `LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL` |
| B3: external resolver bootstrap | `LEAPVIEW_INFISICAL_UNIVERSAL_CLIENT_SECRET` |
| B4: managed-data storage | `LEAPVIEW_MANAGED_DATA_S3_ACCESS_KEY_ID`, `LEAPVIEW_MANAGED_DATA_S3_SECRET_ACCESS_KEY`, `LEAPVIEW_MANAGED_DATA_S3_SESSION_TOKEN` |
| B5: immutable-object storage | `LEAPVIEW_OBJECT_STORE_S3_ACCESS_KEY_ID`, `LEAPVIEW_OBJECT_STORE_S3_SECRET_ACCESS_KEY`, `LEAPVIEW_OBJECT_STORE_S3_SESSION_TOKEN`, `LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_CUSTOMER_KEY` |
| Development-only access inputs | `LEAPVIEW_DEV_API_TOKEN`, `LEAPVIEW_DEV_BOOTSTRAP_TOKEN` |
| Example external source input, not proof of a production env resolver | `LEAPVIEW_WAREHOUSE_DSN` |
| Disposable PostgreSQL/HA qualification fixtures | `LEAPVIEW_POSTGRES_HA_REPLICATION_PASSWORD`, `LEAPVIEW_POSTGRES_HA_REWIND_PASSWORD`, `LEAPVIEW_POSTGRES_HA_SUPERUSER_PASSWORD`, `LEAPVIEW_POSTGRES_BOOTSTRAP_PASSWORD`, `LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD`, `LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD`, `LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD`, `LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_PASSWORD`, `LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_PASSWORD`, `LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_PASSWORD`, `LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD`, `LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_PASSWORD` |

### Existing test evidence inspected

| Boundary | Existing test source | What it helps establish; not a new qualification result |
|---|---|---|
| Portable source boundary | [compiler tests](../../internal/project/compiler/project_flat_test.go#L1350) | Authored source rejects target-owned connection credentials. |
| Binding API | [binding API tests](../../internal/analytics/module/connection_bindings_api_test.go#L18) | Metadata-only persistence and response/error redaction. |
| Pool rotation | [rotation tests](../../internal/analytics/connectionbinding/rotation_test.go#L17), [retirement tests](../../internal/analytics/connectionbinding/rotation_retirement_test.go#L13) | Local switching, old-lease draining, validation failure, stale policy and bounded retirement. |
| Agent persistence | [configuration tests](../../internal/agent/configuration_test.go#L36), [HTTP tests](../../internal/agent/http/configuration_test.go#L36) | Test/save/restart, wrong-key behavior, administrator gate and response redaction. |
| Key preservation | [Compose tests](../../internal/app/cli/composectl/controller_test.go#L239), [host tests](../../internal/app/cli/hostinstall/maintenance_native_effects_linux_test.go#L104) | Existing-key preservation in these initialization/rehearsal paths. |

Current-source searches did not locate Kamal, Postmark/SMTP or pgBackRest
implementation in `internal/`, `deploy/` and `.github/` for the inspected Go,
YAML, Markdown, shell and Terraform file types. Treat those integrations as
proposed dependencies until separately traced; this task does not implement them.

Verification completed for this research document:

- Three Astra research passes covered connections, agent/access credentials,
  and deployment/bootstrap/recovery; an independent Astra review checked the
  assembled inventory. Its corrections were applied and confirmed, with no
  remaining material findings in that bounded review.
- All 41 secret-marked configuration declarations are accounted for; all 76
  local evidence links resolve and their line anchors are within file bounds.
- Markdown table shapes and whitespace checks passed.
- No runtime tests, recovery exercises or deployment qualification were run.
  Step 1 adds only this research document; the focused ADR and implementation
  remain subsequent work.
