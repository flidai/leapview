# ADR-0021 Milestone 5 conformance evidence

This is an evidence map, not a completion report. It maps the confirmation
groups in [ADR-0021](../0021-adopt-a-local-first-analytics-development-workflow.md)
and every row in the [analytics development CLI contract](analytics-development-cli-contract.md)
to the delivery issue that owns the work, an existing test or qualification
path, and the current evidence boundary.

The repository's current public archives are recorded as Compose/`leapviewctl`
archives; the installable authoring CLI archive remains release-blocked until
FAI-798 ships. The qualification harness below describes the evidence boundary
for a future or exact public authoring archive and must not be read as proof
that one is currently available.

The matrix describes this checkout. `Automated/local` means that a repository
test or harness exists; it does not assert that an end-to-end journey or a
release has passed. `External platform/manual` means that the requirement needs
a real Docker host, browser approval, upstream, or prepared target and no
retained external result is claimed. `Release-blocked` means the qualification
contract requires an exact public archive or a still-planned surface; no
released evidence, measurement, approval, or completion is inferred.

## Issue ownership used here

| FAI issue | Scope represented in this matrix |
| --- | --- |
| [FAI-777](https://linear.app/flid/issue/FAI-777) | Delivery foundation used by guided deployment |
| [FAI-778](https://linear.app/flid/issue/FAI-778) | Versioned local runtime payload |
| [FAI-779](https://linear.app/flid/issue/FAI-779) | Verified and pinned local Docker endpoint |
| [FAI-780](https://linear.app/flid/issue/FAI-780) | Resumable local bootstrap |
| [FAI-781](https://linear.app/flid/issue/FAI-781) | Shared local lifecycle and attachments |
| [FAI-782](https://linear.app/flid/issue/FAI-782) | Init sample fixtures and development-input manifest |
| [FAI-783](https://linear.app/flid/issue/FAI-783) | Strict native local profiles and deterministic selection |
| [FAI-784](https://linear.app/flid/issue/FAI-784) | Local profile application boundary |
| [FAI-785](https://linear.app/flid/issue/FAI-785) | Profile application state and recovery |
| [FAI-786](https://linear.app/flid/issue/FAI-786) | Durable profile-application persistence |
| [FAI-787](https://linear.app/flid/issue/FAI-787) | Connection rotation and retirement |
| [FAI-788](https://linear.app/flid/issue/FAI-788) | Profile setup, runtime-side tests, and upstream-read consent |
| [FAI-789](https://linear.app/flid/issue/FAI-789) | Immutable dev-loop capture, admission, and ordered session updates |
| [FAI-790](https://linear.app/flid/issue/FAI-790) | Preview view identity/coherence |
| [FAI-791](https://linear.app/flid/issue/FAI-791) | Preview diagnostics and candidate transitions |
| [FAI-792](https://linear.app/flid/issue/FAI-792) | Durable deployment operation, source, and target identity |
| [FAI-793](https://linear.app/flid/issue/FAI-793) | Reviewed guided delivery through the existing deploy authorities |
| [FAI-794](https://linear.app/flid/issue/FAI-794) | Deployment selection and reconciliation |
| [FAI-795](https://linear.app/flid/issue/FAI-795) | End-to-end lifecycle, profile, preview, and deployment qualification |
| [FAI-796](https://linear.app/flid/issue/FAI-796) | Startup/edit measurements and new-author usability evidence |
| [FAI-797](https://linear.app/flid/issue/FAI-797) | Migration guidance and released-workflow readiness evidence |
| [FAI-798](https://linear.app/flid/issue/FAI-798) | Installable native authoring CLI archive |
| [FAI-799](https://linear.app/flid/issue/FAI-799) | Owner-scoped development-session pointer and stable preview routes |

## ADR-0021 Confirmation groups

| Group | Confirmation requirement | Owning FAI issue(s) | Existing local evidence / harness | Status |
| --- | --- | --- | --- | --- |
| C1 clean-machine authoring journey | Init sample, working dashboard, edit/repair, retained restart, and delivery without manual database setup or copied IDs | FAI-778, FAI-780, FAI-782, FAI-792–FAI-798 | `deploy/local/qualification/qualify.sh`; `deploy/local/qualification_test.go`; `internal/app/cli/projectinit/init_test.go`; `internal/app/cli/localruntime/lifecycle_test.go`; `internal/app/cli/deploy_operation_test.go` | Release-blocked |
| C2 lifecycle and architecture | Isolation, ports, interrupted/repeat bootstrap, version compatibility, scoped stop/reset, local default, explicit remote mode, Docker ambient targeting, joins/exits/crashes | FAI-778–FAI-781, FAI-798 | `internal/app/cli/localdocker/endpoint_test.go`; `internal/app/cli/composectl/docker_endpoint_test.go`; `internal/app/cli/localruntime/controller_test.go`; `internal/app/cli/localruntime/lifecycle_test.go`; `internal/app/cli/local_dev_dispatch_test.go` | Automated/local |
| C3 preview behavior | Stable navigation, compatible UI state, diagnostics, last-valid behavior, exact candidate/query identity, obsolete-result rejection | FAI-789–FAI-791, FAI-799 | `internal/project/developmentsession/session_test.go`; `internal/project/developmentsession/http/handler_test.go`; `internal/project/devloop/service_test.go`; `web/components/dashboard/visualization/signal-envelope.test.ts` | Automated/local |
| C4 development data | Repeatable bounded fixtures, schema/reference behavior, explicit refresh, safe reuse, and no production data/credential transfer | FAI-782, FAI-788 | `internal/project/developmentinput/manifest_test.go`; `internal/project/developmentinput/schema_test.go`; `internal/manageddata/cli/data_plan_test.go`; `internal/app/integration_minio_source_test.go` | Automated/local |
| C5 source profiles and reads | Multiple sources, deterministic selection, typed validation, exact logical identity, missing-credential behavior, runtime diagnostics, redaction, bundle exclusion, and explicit refresh/live-read distinction | FAI-783–FAI-788 | `internal/project/developmentprofile/profile_test.go`; `internal/project/developmentprofile/profile_unix_test.go`; `internal/app/cli/authoring_profile_test.go`; `internal/analytics/module/development_profile_api_test.go`; `internal/analytics/connectionbinding/profile_application_test.go`; `internal/analytics/connectionbinding/rotation_retirement_test.go` | Automated/local |
| C6 preview transition under load | Several queries for A remain pinned while B arrives; delayed A results cannot mix into B | FAI-789–FAI-791, FAI-799 | `web/components/dashboard/visualization/signal-envelope.test.ts`; `web/components/dashboard/builder-visualization-state.test.ts`; `internal/project/devloop/service_test.go` | Automated/local |
| C7 guided delivery | Same source identity through guided steps, target binding, independent production qualification, approval gates, stale rejection, lost-ack recovery, and accurate pending/active reporting | FAI-777, FAI-792–FAI-797 | `internal/app/cli/delivery_cli_test.go`; `internal/project/cli/publish_command_test.go`; `internal/app/cli/publish_test.go`; `internal/app/cli/deploy_operation_test.go`; `deploy/compose/QUALIFICATION.md` | External platform/manual |
| C8 delivery selection | Interrupted publication with edits, competing checkpoints, deterministic automation, and missing/ambiguous handles | FAI-792–FAI-794 | `internal/project/cli/operation_descriptor_test.go`; `internal/project/cli/deploy_command_test.go`; `internal/app/cli/deploy_operation_test.go` | Automated/local |
| C9 measurements | Cold cached/uncached, warm restart, edit-to-visible p50/p95, representative semantic/model/dashboard/invalid-edit cases, fixture/network metadata, and ADR-0004 gate | FAI-795–FAI-797, FAI-799 | `deploy/local/qualification/qualification-contract.json` records these as `not-run`; no released preview measurement harness currently supplies samples | Release-blocked |
| C10 usability | New author and teammate fresh-checkout validation | FAI-795–FAI-797 | No retained repository or external usability study evidence; the qualification workflow is not a substitute | External platform/manual |

## CLI contract rows

| # | Required scenario | Owning FAI issue(s) | Existing local evidence / harness | Status |
| ---: | --- | --- | --- | --- |
| 1 | Remote active Docker context, `DOCKER_HOST`, or `DOCKER_CONTEXT` | FAI-779 | `internal/app/cli/localdocker/endpoint_test.go`; `internal/app/cli/local_dev_dispatch_test.go` | Automated/local |
| 2 | Misleading context name or loopback tunnel | FAI-779 | `internal/app/cli/localdocker/endpoint_test.go`; a forwarded daemon deliberately bound at a recognized socket remains an external adversarial platform case | External platform/manual |
| 3 | Supported local Engine and Docker Desktop | FAI-778, FAI-779, FAI-798 | `internal/app/cli/localdocker/endpoint_test.go`; `deploy/local/qualification/qualify.sh` | External platform/manual |
| 4 | Docker context changes during startup | FAI-779 | `internal/app/cli/composectl/docker_endpoint_test.go`; `internal/app/cli/localdocker/endpoint_test.go` | Automated/local |
| 5 | PostgreSQL and object-storage sources in one local profile | FAI-783–FAI-786, FAI-788 | `internal/project/developmentprofile/profile_test.go`; `internal/app/integration_minio_source_test.go`; `internal/analytics/connectionbinding/profile_application_test.go` | Automated/local |
| 6 | Profile-file/name selection, missing files, or conflicting remote flags | FAI-783 | `internal/project/developmentprofile/profile_test.go`; `internal/app/cli/local_dev_dispatch_test.go` | Automated/local |
| 7 | Invalid schema, duplicate keys, unsupported version, or secret-bearing URL | FAI-783 | `internal/project/developmentprofile/profile_test.go`; `internal/project/developmentprofile/schema_test.go`; `internal/app/cli/authoring_profile_test.go` | Automated/local |
| 8 | Missing credential, unselected host secret, or configured Infisical | FAI-783, FAI-784, FAI-788 | `internal/project/developmentprofile/profile_test.go`; `internal/app/cli/authoring_profile_test.go`; `internal/analytics/environment/resolver_test.go` | Automated/local |
| 9 | Different profiles attach to one checkout | FAI-784–FAI-786 | `internal/analytics/connectionbinding/profile_application_test.go`; `internal/analytics/connectionbinding/profile_application_service_test.go`; `internal/app/cli/authoring_test.go` | Automated/local |
| 10 | A covers commerce/inventory; B omits still-required inventory | FAI-783–FAI-786 | `internal/project/developmentprofile/profile_test.go`; `internal/analytics/connectionbinding/profile_application_test.go`; `internal/analytics/connectionbinding/profile_application_service_test.go` | Automated/local |
| 11 | Graph gains a required external connection | FAI-783–FAI-786 | `internal/project/developmentprofile/profile_test.go`; `internal/analytics/connectionbinding/profile_application_test.go` | Automated/local |
| 12 | Second binding fails after first changed; CLI or runtime crashes | FAI-784–FAI-786 | `internal/analytics/connectionbinding/profile_application_service_test.go`; `internal/analytics/connectionbinding/postgres/profile_application_repository_test.go`; `internal/app/cli/authoring_lifecycle_test.go` | Automated/local |
| 13 | Crash after all changes but before completion acknowledgement | FAI-785, FAI-786 | `internal/analytics/connectionbinding/profile_application_test.go`; `internal/analytics/connectionbinding/profile_application_service_test.go`; `internal/platform/postgres/migrations/migrations_test.go` | Automated/local |
| 14 | Recovery sees edited YAML, different bundles, missing checkpoint, or revision drift | FAI-785, FAI-786 | `internal/analytics/connectionbinding/profile_application_test.go`; `internal/analytics/connectionbinding/profile_application_service_test.go`; `internal/app/cli/authoring_test.go` | Automated/local |
| 15 | Same profile and variable name in two terminals, different bundles | FAI-784–FAI-786 | `internal/analytics/connectionbinding/profile_application_service_test.go`; `internal/app/cli/authoring_profile_test.go`; `internal/app/cli/localruntime/lifecycle_test.go` | Automated/local |
| 16 | Explicit secret rotation versus principal/scope replacement | FAI-787 | `internal/analytics/connectionbinding/rotation_test.go`; `internal/analytics/connectionbinding/rotation_retirement_test.go`; `internal/analytics/connectionbinding/runtime_bindings_test.go` | Automated/local |
| 17 | Switch away from a credential-bearing connection with in-flight readers | FAI-787 | `internal/analytics/connectionbinding/rotation_retirement_test.go`; `internal/analytics/connectionbinding/rotation_test.go` | Automated/local |
| 18 | Connection works on host but not in Docker | FAI-779, FAI-788 | `internal/app/integration_minio_source_test.go`; `internal/analytics/duckdb/source_test.go`; `deploy/local/qualification/qualify.sh` | External platform/manual |
| 19 | Guided setup, manual profile editing, and repeated application | FAI-783–FAI-786, FAI-788 | `internal/project/developmentprofile/profile_test.go`; `internal/app/cli/authoring_profile_test.go`; `internal/analytics/module/development_profile_api_test.go` | Automated/local |
| 20 | Approved remote reads and subsequent YAML edits | FAI-788 | `internal/app/cli/authoring_test.go`; `internal/project/devloop/service_test.go`; no retained external upstream-read result | External platform/manual |
| 21 | Local profile followed by production deployment | FAI-783–FAI-788, FAI-792–FAI-794 | `deploy/local/authoring_package_test.go`; `deploy/local/qualification_test.go`; `internal/project/cli/operation_descriptor_test.go` | External platform/manual |
| 22 | Two sessions; one receives Ctrl-C | FAI-781 | `internal/app/cli/localruntime/lifecycle_test.go`; `internal/app/cli/authoring_lifecycle_test.go` | Automated/local |
| 23 | Crash or concurrent join/last exit | FAI-781 | `internal/app/cli/localruntime/lifecycle_test.go` | Automated/local |
| 24 | Stop/reset with attachments or a racing join | FAI-781 | `internal/app/cli/localruntime/lifecycle_test.go`; `internal/app/cli/authoring_lifecycle_test.go` | Automated/local |
| 25 | Several dashboard queries for A run while B arrives | FAI-789–FAI-791, FAI-799 | `web/components/dashboard/visualization/signal-envelope.test.ts`; `web/components/dashboard/builder-visualization-state.test.ts` | Automated/local |
| 26 | A loses publication acknowledgement; files change; bare deploy runs | FAI-792–FAI-794 | `internal/app/cli/deploy_operation_test.go`; `internal/project/cli/operation_descriptor_test.go` | Automated/local |
| 27 | Multiple checkpoints or concurrent invocations | FAI-792–FAI-794 | `internal/project/cli/operation_descriptor_test.go`; `internal/app/cli/deploy_operation_test.go` | Automated/local |
| 28 | Headless missing/unknown handle, conflicting flags, or changed target alias | FAI-792–FAI-794 | `internal/project/cli/deploy_command_test.go`; `internal/project/cli/operation_descriptor_test.go` | Automated/local |
| 29 | Resume from retained CI artifact | FAI-793, FAI-794 | `internal/project/cli/operation_descriptor_test.go`; `internal/app/cli/deploy_operation_test.go` | Automated/local |
| 30 | Pending approval, stale plan, or unresolved activation | FAI-777, FAI-792–FAI-797 | `internal/app/cli/deploy_operation_test.go`; `internal/app/cli/delivery_cli_test.go`; `deploy/compose/QUALIFICATION.md` | External platform/manual |

The required scenarios remain acceptance requirements. A path listed above is
not a passing result, and a local test is not external platform evidence. The
release qualification report is the authority for an exact public archive;
until it is run against that archive, do not describe the authoring journey as
released or complete.
