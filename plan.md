# LeapView CLI improvement plan

Reviewed: 6 October 2026
Repository baseline: `b15f86f973c315861467ef927909e8b0a8333ebc`
Project: [LeapView CLI Usability, Diagnostics &amp; Agent Guidance](https://linear.app/flid/project/leapview-cli-usability-diagnostics-and-agent-guidance-223c86ae3130)
Lead: Ganesh Kambli
Target: 13 October 2026

Implementation was approved on 6 October 2026. The project is In Progress, led by Ganesh Kambli, and all five implementation issues are assigned to him. The linked issues carry their current delivery status. The acceptance gates below remain separate from code implementation and PR review.

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

Run two independent agent usability trials, as requested by Ganesh during implementation: one approaches the CLI as a new author and one uses a fresh checkout. Both must discover help, diagnose a seeded prerequisite issue, attempt the sample, repair an invalid edit, and distinguish pending approval from active deployment using the CLI’s own guidance. Record observed outcomes, friction, and any unavailable release/runtime prerequisites. These are agent trials, not human sessions; human sessions are no longer a completion requirement for this project.

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

Completion requires passing tests/CI, accurate generated docs, migrated callers, two recorded independent agent usability trials, and recorded qualification results. Unavailable release prerequisites stay explicitly unresolved in existing conformance evidence; source-level tests cannot close those gates.

## Implementation issues

1. [FAI-1129: Make CLI discovery reliable and root invocation help-only](https://linear.app/flid/issue/FAI-1129/make-cli-discovery-reliable-and-root-invocation-help-only)
2. [FAI-1130: Standardize CLI result formats, errors, interaction, and cancellation](https://linear.app/flid/issue/FAI-1130/standardize-cli-result-formats-errors-interaction-and-cancellation)
3. [FAI-1131: Add read-only local and explicit remote CLI doctor](https://linear.app/flid/issue/FAI-1131/add-read-only-local-and-explicit-remote-cli-doctor)
4. [FAI-1132: Generate offline --llms guidance from the shared CLI catalog](https://linear.app/flid/issue/FAI-1132/generate-offline-llms-guidance-from-the-shared-cli-catalog)
5. [FAI-1133: Qualify the CLI workflow and finish documentation and caller migration](https://linear.app/flid/issue/FAI-1133/qualify-the-cli-workflow-and-finish-documentation-and-caller-migration)

Issues are assigned to Ganesh Kambli and linked in delivery order. Code is delivered as five dependent PRs, with independent automated review and the repository’s required CI, security, and merge-queue checks.


## 6. Implementation review and evidence

The approved implementation uses existing Cobra commands, domain error/result types, local-runtime parsing, and release qualification. It adds no dependencies or legacy aliases. The website manifest and offline guidance share one pure command catalog; schema 2 replaces the previous manifest contract in all consumers.

Independent review found and corrected these problems:

* Contributor launchers still started the server through bare `leapview`; initial launch, restart, and Air now invoke `serve`, with maintained launcher tests updated.
* Interactive deployment could wait indefinitely after cancellation. Prompt reads now stop waiting on context cancellation, and missing headless intent is rejected before credential resolution.
* Hosted historical-transition qualification found that the client interruption boundary changed graceful server shutdown to exit 143. The boundary now preserves `serve`'s actual outcome: successful shutdown exits 0 and shutdown errors remain failures; interrupted client commands retain 130/143. The regression reproduced the old failure before passing, and existing real signal subprocess tests also passed. The flag-migration contract assertion is included in the same PR as the new flag.
* Progress and result writers ignored failures. A failed write now returns an error; a successfully persisted local runtime remains applied even if its readiness message cannot be written.
* Validation and agent failures could emit a second diagnostic or return success. Reported domain results now preserve failure status without duplicate output.
* Runtime-package identity checks did not verify asset contents. Doctor now checks the four runtime assets against the installed package’s adjacent `SHA256SUMS`, rejecting missing, duplicate, malformed, unsafe, or mismatched entries.
* Compilation does not accept a context. Doctor stops waiting at its deadline using a bounded worker; the read-only compiler can finish after that deadline. Making compilation itself cancellable remains the documented improvement if this ceiling matters.
* Archive qualification originally checked too little discovery content. It now requires grouped help, authoring examples, offline guidance, a complete doctor JSON report, consistent failure status, and no created CLI state.
* A migrated Python caller lost indentation; parsing and the affected qualification tests caught it and the indentation was restored.

Observed checks are recorded below and in the final PRs. A successful source test or local candidate archive smoke is not public release evidence. Local `task ci` encountered an unavailable shared Docker bridge while provisioning its disposable PostgreSQL topology; required hosted checks must pass before merging.

The requested independent agent usability trials are complete and recorded below. Remaining acceptance work requires exact public archive/platform qualification and the existing lifecycle/preview/measurement evidence. Do not mark the project or FAI-1133 complete until those required observations are recorded. Agent trial results must be identified as agent observations, with environment blockers stated rather than treated as successful journeys.


### Delivery and verification

| Slice | Pull request |
| --- | --- |
| A: discovery | [#875](https://github.com/flidai/leapview/pull/875) |
| B: output/errors/interaction | [#878](https://github.com/flidai/leapview/pull/878) |
| C: read-only doctor | [#879](https://github.com/flidai/leapview/pull/879) |
| D: shared catalog and offline guidance | [#880](https://github.com/flidai/leapview/pull/880) |
| E: qualification and trial corrections | [#881](https://github.com/flidai/leapview/pull/881) |

Native GitHub stack 882 preserves this order and targets main. Each PR has its own review; required CI/security checks and the normal merge queue govern delivery.

Passed locally after rebasing onto main: the full app CLI, local runtime, project initialization, project/access/agent/managed-data CLI, CLI API, shared catalog, generator, website HTTP, and archive-harness suites (12 packages). Generated snapshots, documentation checks, and quality-budget checks passed. Current caller tests passed: 20 Python publication tests (one skip), 17 Python qualification tests, and 35 Bun caller/launcher contracts. Focused checks passed again after the trial corrections. `task ci` reached its required PostgreSQL baseline test and failed because the shared Docker bridge is missing; this infrastructure failure is not waived.

Both requested independent agent trials are recorded in [the usability evidence](reviews/cli-agent-usability.md). They discovered the workflow, initialized and validated the sample, repaired invalid authored YAML, and interpreted pending versus active delivery from guidance. Actual preview and target delivery were unavailable. Review corrected the init directory precondition, explained JSON diagnostic streams explicitly, pointed doctor failures to validation, and moved archive state snapshots before every executable probe, including version/help.

Keep FAI-1133 and the project In Progress while exact public archive/platform and existing lifecycle/preview/measurement qualification remain outstanding. Do not replace those observations with source tests or agent interpretations of deployment guidance.

## Continuation (7 October 2026)

Inspected all five project issues, their linked PRs and reviews, the retained CLI branches, and the clean c2e8 checkout at `7437799c92bfe962c9d083530ce05f180d23d92b`. PRs #875, #878, #879, #880 and #881 are merged into main, and their latest hosted CI gates passed. FAI-1129–1132 are now Done. FAI-1133 remains In Progress: its implementation and the two agent trials are delivered, while exact current package/platform and real preview/lifecycle/measurement observations remain outstanding.

The next qualification step exposed a maintained caller failure: enabling `run_lifecycle` in `authoring-package-qualification.yml` passes removed `--manual-prerequisites-confirmed` and exits 2 before archive or runtime qualification. Remove that argument and correct the obsolete manual-authentication descriptions. An executable regression runs the workflow's actual Bash step against the harness parser in both modes, including paths with spaces, required mode, and the explicit Docker socket. Before the fix, static mode passed and lifecycle mode failed on the removed argument. The full `deploy/local` suite and workflow `actionlint` pass after the fix.

The current VPS has Docker Engine 29.8.0, Compose 5.4.0 and an active `docker0` bridge. The required PostgreSQL baseline preparation test passed during this continuation's `task ci` run; the earlier missing-bridge failure remains historical evidence. This source validation is separate from released-package lifecycle qualification.

Full `task ci` did not pass: the unchanged APIGen TypeScript test `rejects invalid command contracts before writing IR` exceeded its 30-second limit (72 other tests passed). The same test timed out when run alone. No APIGen source was changed in this continuation. Retained logs are `.tmp/fai-1133/task-ci.log` and `.tmp/fai-1133/apigen-timeout-recheck.log`; the focused qualification suite, workflow lint, Python syntax check and patch whitespace check passed. The caller fix is reviewable, with full CI and the existing acceptance gates still unresolved.

The newest public authoring release remains `v0.3.0-alpha.1` (24 September), and the newest [release workflow candidate](https://github.com/flidai/leapview/actions/runs/37443301375) (revision `58ab08d3c5652eeac61402a4f1a17865436d4e6a`) also predates the CLI merges. Its four native authoring archives are retained, but cannot establish acceptance for the updated CLI. No authoring-package qualification workflow run was found. Next, build matching current artifacts and qualify an exact archive containing the merged CLI changes, retaining its checksum, identity, platform and lifecycle results; real preview scenarios and measurements still require their own observed evidence. The project stays In Progress.
