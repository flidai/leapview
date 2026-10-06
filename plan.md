# LeapView CLI improvement plan

Reviewed: 6 October 2026
Repository baseline: `b15f86f973c315861467ef927909e8b0a8333ebc`
Project: [LeapView CLI Usability, Diagnostics &amp; Agent Guidance](https://linear.app/flid/project/leapview-cli-usability-diagnostics-and-agent-guidance-223c86ae3130)
Lead: Ganesh Kambli
Target: 13 October 2026

This document preserves the reviewed plan from the planning discussion. Creating the project does not imply code implementation, release qualification, or usability acceptance has completed.

## 1. Objective and decisions

Make LeapView's CLI discoverable, predictable for automation, and useful for diagnosing problems. Review the entire public command surface, prioritizing analytics authors using init → dev → validate → deploy.

Jacob's screenshots motivate a design review; doctor and --llms are additions within that broader work.

Confirmed decisions:

* Bare `leapview` displays workflow help. Server startup requires `leapview serve`.
* Commands offering selectable text/JSON results use `--format text|json`. Remove their Boolean `--json` flags and update affected callers.
* Introduce no compatibility aliases, old/new interfaces, or fallback behavior.
* Preserve the existing local-development and target-owned deployment architecture.
* If this document is subsequently saved as root `plan.md`, first preserve the completed CodeQL plan as `plan.codeql-remediation.md`.

Success means a new author can discover the workflow, diagnose prerequisites, preview a project, and understand deployment outcomes without learning internal implementation details. CI and agents receive documented output and exit behavior without unexpected interaction.

## 2. Research and repository findings

The governing design is [ADR-0021](https://github.com/flidai/leapview/blob/b15f86f973c315861467ef927909e8b0a8333ebc/adr/0021-adopt-a-local-first-analytics-development-workflow.md) and the [analytics-development CLI contract](https://github.com/flidai/leapview/blob/b15f86f973c315861467ef927909e8b0a8333ebc/adr/specifications/analytics-development-cli-contract.md). Local runtime management, explicit remote development, durable operation handles, exact-plan confirmation, and deployment recovery already have implementations and tests.

External research supports clear help, separate result/diagnostic streams, explicit interaction rules, and useful failure messages:

* [CLI Guidelines](https://clig.dev/)
* [Cobra user guide](https://github.com/spf13/cobra/blob/main/site/content/user_guide.md)
* [Cobra command-derived documentation](https://cobra.dev/docs/how-to-guides/clis-for-llms/)
* [Homebrew diagnostic-command precedent](https://docs.brew.sh/Manpage)
* [llms.txt proposal](https://llmstxt.org/)

The llms.txt proposal concerns website documentation and does not define a CLI --llms flag; LeapView must define that interface.

| Finding | Evidence and implication |
| -- | -- |
| Root invocation starts services | NewCommand calls runServe without a subcommand. Replace this with help. |
| Discovery depends on environment parsing | Command construction resolves paths through config.MustLoad, which can panic. Help must work independently of operational configuration. |
| Output conventions differ | Validation, version, search, and agent commands use --json; authoring commands use --format. |
| Local JSON can contain prose | Profile reporting and runtime startup use the same stdout as development results. |
| Error handling loses distinctions | main uses log.Fatal although deployment has typed pending/failure/indeterminate outcomes. |
| Some adapters bypass command streams | API commands write to os.Stdout and raw response handling ignores body-read errors. |
| Documentation already has a foundation | clidocgen derives a manifest from Cobra and safety annotations. Extend it rather than create a second registry. |
| Diagnostic guidance is misleading | Troubleshooting describes remote plan as read-only, although it creates durable target state. |
| Implementation is not release evidence | The conformance matrix records outstanding archive, platform, measurement, and usability evidence. |

The CLI already applies a five-minute HTTP timeout by modifying http.DefaultClient. Improve client ownership and testability; do not describe this as a missing timeout.

## 3. Implementation sequence

Deliver five focused changes in order. Each includes its own relevant documentation and tests.

### A. Make discovery reliable

* Root invocation prints grouped help and exits successfully without loading operational configuration, contacting services, or creating files.
* Group public commands into Authoring, Delivery, Data and Query, Access, Operations, and Reference; retain existing command paths.
* Add concise examples and useful next steps to authoring and diagnostic commands.
* Move configuration, credential-store, checkpoint-path, and runtime initialization into execution. Invalid configuration returns ordinary errors, not panics.
* Give every runnable command explicit positional-argument validation; correct required arguments advertised as optional.
* Initialize Cobra's built-in help/completion before catalog generation and assign groups and metadata explicitly.
* Add offline completion for formats, local target names, and generated API operation names; no authentication or target contact.
* Audit maintained server launchers for implicit root startup and update them to serve.

### B. Establish output, error, and interaction contracts

Output:

* Replace Boolean --json switches with --format text|json on public commands that offer selectable result output; retain existing success payloads.
* Preserve artifact encoding meanings for schema and Ossie exports. Do not add a conflicting global format flag.
* Document fixed JSON/raw-output commands explicitly.
* Results use command stdout; progress, prompts, and diagnostics use command stderr, including local startup/profile output.
* Finite JSON results are one complete document. Development watch and device-login events remain newline-delimited JSON.
* Propagate writer and response-body errors. Tests parse complete output without discarding leading prose.

Failures:

* Replace timestamped log.Fatal with one CLI error-rendering boundary.
* Preserve existing structured domain results; do not append another result or duplicate an already-reported failure.
* Failures without domain results include command, error code, message, and available next action. Use JSON stderr when JSON output is selected.
* Exit codes: 0 successful command completion; 1 execution/validation/authentication/diagnostic failure; 2 invalid invocation or command selection; 3 guided deployment awaiting confirmation/approval; 4 guided deployment indeterminate; 130/143 client interruption by SIGINT/SIGTERM.
* Deploy success means confirmed activation. Plan/build/publish success means completion of that command's operation; successful publication submission is not proof of active deployment.

Interaction:

* Add --no-input as an explicit automation policy. JSON mode also disables terminal prompts and automatic browser opening.
* Prompt only when stdin and the prompt destination are terminals; missing required input fails with the next action.
* Preserve device-login JSON events and bounded authentication. Device login is not unattended CI authentication.
* Preserve exact-plan confirmation, explicit new/resume intent, retained handles, and independent server approval.
* Propagate cancellation through HTTP and child processes while preserving graceful server shutdown and development-session detach.
* Replace global HTTP-client mutation with explicit CLI clients, retaining the five-minute default and specialized shorter timeouts.

### C. Add read-only leapview doctor

Local interface:

```text
leapview doctor [--source-root PATH]
                [--profile-file PATH] [--profile NAME]
                [--docker-context NAME | --docker-host URI]
                [--format text|json] [--timeout DURATION]
```

Explicit remote interface:

```text
leapview doctor --target NAME_OR_URL
                [--token TOKEN]
                [--format text|json] [--timeout DURATION]
```

Default local checks:

* CLI identity, supported platform, and runtime-package integrity/version agreement.
* Docker and Compose availability and effective endpoint selection through existing verified-local-endpoint rules.
* Source-root validation and selected profile validity.
* Presence and structural validity of required credential environment variables without revealing values.
* Existing checkout-owned runtime state, attachment consistency, and service status through inspection-only adapters.
* Outside an analytics project, implicit project checks skip with explanation; an explicit missing/invalid source or profile path fails.

Remote checks:

* Require --target; ambient targets cannot redirect local diagnosis.
* Check readiness, public instance identity, and agreement with saved metadata.
* Check authenticated capabilities/current principal only with an explicitly supplied token or the existing API-token environment setting.
* Do not read native OAuth secrets, refresh credentials, exchange workload credentials, or initiate login. Skipped authentication must not appear verified.
* Reject remote mode combined with local Docker/profile flags.

Report:

* Ordered report with schemaVersion, overall status, and stable check IDs; each check has pass/warn/fail/skip, explanation, and remediation.
* Required check failures exit 1; warnings/inapplicable checks alone exit 0; invocation errors exit 2.
* Overall timeout defaults to 30 seconds; external probes are bounded to at most five seconds each. Failed prerequisites cause dependent checks to skip.
* No image pulls, container starts, state-directory/lock-file creation, profile mutation, upstream data connection tests, or deployment-plan creation.
* Extract inspection-only functions from existing parsing/validation logic when lifecycle or authentication helpers mutate state.
* Keep healthcheck as the narrow container-readiness command.

### D. Add offline leapview --llms

* One root-only documentation flag. Combining it with an executable subcommand fails before effects.
* Emit deterministic Markdown and exit successfully without Docker, credentials, valid runtime configuration, a checkout, or network.
* Include binary version, workflow examples, compact public-command index, target rules, side effects, confirmations, output modes, exit codes, and recovery guidance.
* Point detailed inspection to command help and existing machine-readable documentation endpoints.
* Extract a shared pure catalog builder from clidocgen for website reference generation and --llms.
* Derive syntax, flags, defaults, examples, visibility, and safety from the finalized Cobra tree. Exclude hidden commands/flags.
* Include root flags in generated documentation and distinguish single JSON documents, event streams, and raw output in metadata.
* Update the manifest and website/MCP consumers together; no alternate parsers or duplicate catalogs.
* Cover every feature-owned CLI package and the shared catalog in generation dependencies to prevent stale docs.

### E. Update callers and qualify the experience

* Update owned scripts, CI examples, package verification, archive qualification, guides, and tests affected by --json removal or explicit serve.
* Migrate LeapView invocations only; do not change unrelated tools' --json flags.
* Replace misleading troubleshooting with doctor/validation/readiness before state-changing commands.
* Explain contributor task dev versus analytics-author leapview dev.
* Retain existing delivery, project identity, approvals, Docker matrix, and lifecycle ownership.
* Use existing release qualification. Installer redesign, new platform support, automatic repair, and new delivery state machines are out of scope.

## 4. Test plan and acceptance

Use red-green-refactor for features and fixes.

| Area | Required coverage |
| -- | -- |
| Discovery | Root/help/version/completion/--llms under missing configuration, malformed unrelated environment, no Docker, no credentials; no operational initialization. |
| Arguments | Invalid flags, extra arguments, conflicting selectors, missing values fail before mutation. |
| Output | Parse entire stdout; startup prose only on stderr; correct finite/event formats; writer failures propagate. |
| Failures | Usage, validation, authentication, pending approval, indeterminate publication, cancellation, body-read failures; correct exit status without duplicate output. |
| Interaction | Pipes/--no-input never prompt; JSON never opens browsers; interactive approval remains tied to the exact plan. |
| Doctor | Missing Docker, remote contexts, unsupported endpoints, corrupt state, invalid profile, missing credentials, unhealthy services, identity mismatch, timeout; assert no mutating calls/filesystem changes. |
| Agent docs | Deterministic output, complete visible-command coverage, hidden flags omitted, root flags included, examples resolve against command tree. |
| Workflows | Local dev ignores ambient production targets; remote dev starts no containers; bad edits retain working result; resume uses retained source; shared-session shutdown preserved. |
| Packaging | Current scripts use new flags; installed help/doctor/--llms work without contributor toolchain. |

Prepare generated dependencies, run focused CLI/domain tests, documentation checks, task generated:check, and task ci. Run existing Docker/authoring qualification for lifecycle changes.

Run two observed usability sessions: a new author and a teammate using a fresh checkout. Both must discover help, diagnose a seeded prerequisite issue, run the sample, repair an invalid edit, and distinguish pending approval from active deployment without maintainer intervention.

Planning-time verification:

* Passed internal/app/cli/localdocker and internal/platform/cliapi.
* Application/project CLI, documentation-generator, and local-runtime tests could not execute because generated packages were absent.
* Initial testing also hit exhausted /tmp inodes; another temporary directory enabled the independent passing suites.
* No full CI, Docker qualification, usability study, or released-archive acceptance is claimed.

## 5. Final review corrections and completion criteria

| Reviewed risk | Incorporated correction |
| -- | -- |
| Rebuilding an existing workflow | Improve ADR-0021 implementation while preserving authorities. |
| Doctor mutating indirectly | Inspection-only adapters; avoid refresh, lifecycle locks, and planning APIs. |
| Treating all JSON as one document | Preserve login/watch streams. |
| Conflicting export formats | Normalize selectable results without a global format flag. |
| Separate agent docs drifting | Share command-derived catalog. |
| Help requiring valid configuration | Defer operational initialization. |
| Packaging broken by flag migration | Update current scripts and qualification with the change. |
| False missing-timeout claim | Preserve existing timeout while removing global mutation. |
| Code mistaken for release proof | Require exact artifact/platform evidence and retain unresolved gates. |

Completion requires passing tests/CI, accurate generated docs, migrated callers, two successful usability sessions, and recorded qualification results. Unavailable release prerequisites stay explicitly unresolved in existing conformance evidence; source-level tests cannot close those gates.

## Implementation issues

1. [FAI-1129: Make CLI discovery reliable and root invocation help-only](https://linear.app/flid/issue/FAI-1129/make-cli-discovery-reliable-and-root-invocation-help-only)
2. [FAI-1130: Standardize CLI result formats, errors, interaction, and cancellation](https://linear.app/flid/issue/FAI-1130/standardize-cli-result-formats-errors-interaction-and-cancellation)
3. [FAI-1131: Add read-only local and explicit remote CLI doctor](https://linear.app/flid/issue/FAI-1131/add-read-only-local-and-explicit-remote-cli-doctor)
4. [FAI-1132: Generate offline --llms guidance from the shared CLI catalog](https://linear.app/flid/issue/FAI-1132/generate-offline-llms-guidance-from-the-shared-cli-catalog)
5. [FAI-1133: Qualify the CLI workflow and finish documentation and caller migration](https://linear.app/flid/issue/FAI-1133/qualify-the-cli-workflow-and-finish-documentation-and-caller-migration)

Issues begin in Todo and are linked in delivery order. Project lead ownership does not automatically assign every implementation issue.
