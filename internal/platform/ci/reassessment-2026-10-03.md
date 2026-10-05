# Repository-wide CI/CD reassessment — 3 October 2026

The [CI/CD Quality & Speed project](https://linear.app/flid/project/leapview-cicd-quality-and-speed-8a7e28e7113f/overview)
was reassessed at main `2865ef5766aa56e8157de267d8393652f1f9d7f2` after its
original 17 issues and eight implementation PRs completed. This follow-up covers
all 27 maintained workflows, release and deployment boundaries, reporting, and
live repository governance. The original measured cache decisions remain valid;
whole-CI latency and deployment governance need separate follow-through.

## Workflow names

Use `Category / Purpose` so the Actions list distinguishes validation, artifact
builds, qualification, releases, deployments, infrastructure, and maintenance.
The two public-site image workflows build and publish images; they do not activate
the permanent site. Automatic site activation remains intentionally deferred.

Workflow filenames, job IDs, triggers, permissions, and required check names are
stable. The protected main ruleset requires `CI gate` and `Security gate`;
`Electron gate` remains the exact-candidate native proof. Reusable host-recovery
name normalization and trusted-builder display labels move with the names.
`github.workflow`-based concurrency keys change once with their display name;
workflow-file cache identities invalidate conservatively. Historical evidence
retains the workflow names observed when it was collected.

| Workflow file under `.github/workflows/` | Actions display name |
| --- | --- |
| `artifacts.yml` | Build / Main image |
| `authoring-package-qualification.yml` | Qualification / Authoring package |
| `ci-health.yml` | Maintenance / CI health |
| `ci.yml` | CI / Pull requests |
| `dbt-warehouse-boundary-azure-qualification.yml` | Qualification / dbt Azure boundary |
| `dbt-warehouse-boundary-reference.yml` | Deploy / dbt warehouse reference |
| `demo-deploy.yml` | Deploy / Hosted demo |
| `demo-upgrade-qualification.yml` | Qualification / Host recovery and migration |
| `desktop-preview-release.yml` | Release / Desktop unsigned preview |
| `electron-security-proof.yml` | Security / Electron proof |
| `hetzner-deploy.yml` | Deploy / Ephemeral Hetzner |
| `installed-candidate.yml` | Qualification / Installed candidate |
| `localdocker-macos.yml` | CI / Local Docker macOS |
| `managed-scaffold.yml` | CI / Managed deployment scaffold |
| `merge-validation.yml` | CI / Merge queue |
| `nightly.yml` | CI / Nightly validation |
| `nix-candidate.yml` | Qualification / Protected Nix candidate |
| `nix-development.yml` | CI / Nix development |
| `orchestration-cache.yml` | Maintenance / Orchestration cache |
| `public-site-smoke.yml` | Qualification / Public installation |
| `recovery-evidence-qualification.yml` | Qualification / Recovery evidence |
| `release.yml` | Release / Server and CLI |
| `security.yml` | Security / Policy and scans |
| `site-deploy.yml` | Build / Main public site image |
| `site-image.yml` | Build / Public site image |
| `site-infrastructure.yml` | Infrastructure / Public site |
| `site-kamal-trial.yml` | Qualification / Kamal site image |

GitHub's registry also retains two deleted temporary diagnostic workflows and
managed Copilot/Dependabot entries. They are not maintained YAML workflows and
are not recreated or rewritten by this cleanup. API selectors and OCI provenance
continue to use the stable YAML paths.

## Remaining work

| Finding | Evidence and acceptance | Tracking |
| --- | --- | --- |
| Candidate publication can overwrite stable aliases | The manual release input `image_tag` feeds image metadata before qualification. Supplying `latest` can move a stable alias even if qualification later fails. Remove arbitrary candidate tags; keep stable promotion behind the existing qualified push-only publication. | [FAI-1064](https://linear.app/flid/issue/FAI-1064) |
| Health measurements overstate available evidence | The seven-day report publishes p95 for 13-observation selective and nightly cohorts, below the documented 20-observation minimum. Current recovery-child requirements also misclassify older runs. Make small-sample p95 unavailable and use revision-specific evidence for expected jobs. | [FAI-1065](https://linear.app/flid/issue/FAI-1065) |
| Live deployment environments differ from documented protection | The read-only governance audit fails demo main-only restrictions and required reviewers for demo, ephemeral qualification, and site production. The qualification allow-list retains an unprotected temporary branch; site production allows administrator bypass. Configure owner-designated independent reviewers and documented branch/self-review/bypass policy, then rerun the read-only audit. | [FAI-1066](https://linear.app/flid/issue/FAI-1066) |
| Shared release aliases can move backward | Per-ref concurrency allows separate version releases to qualify concurrently and update the same `latest`/major-minor aliases out of order. Define and enforce stable-channel ordering at promotion. | [FAI-1068](https://linear.app/flid/issue/FAI-1068) |
| Clean local CI requires generation warm-up | The first `task ci` compares absent ignored generated contracts against newly created outputs and fails with a clean tracked tree. Initialize build-only outputs while preserving tracked snapshot drift and nondeterminism checks. | [FAI-1069](https://linear.app/flid/issue/FAI-1069) |
| Whole-CI latency objectives remain open | The seven-day report has full-PR p95 28m38s (212 samples) and merge p95 25m35s (73), above the 12-minute thresholds; reruns are 4.1% against 3%. Rebaseline after reporting corrections, retain canceled audit evidence as inconclusive, and choose further improvements from comparable measurements. | [FAI-1067](https://linear.app/flid/issue/FAI-1067) |

The original read-only report collected 502 runs, with 181 incomplete-evidence
records and six raw selection-audit misses. Reconciliation found all six were
cancelled audit-only jobs in canceled run `36968039247`; none is evidence of a
selector defect. Cancellation is inconclusive and must remain incomplete
evidence, while only observed failures or timeouts count as potential misses.
Historical workflow names and required jobs are now reconstructed from each
run's immutable workflow revision; unavailable provenance stays incomplete. The
first API attempt failed with HTTP 502; a complete retry succeeded. Green
latest-main qualification does not establish a passing rolling latency SLO.

The corrected seven-day collection (3 October 2026) contains 515 runs, 203
incomplete-evidence records, and zero potential audit misses across 15 audit
samples. All available immutable workflow sources were fetched and parsed
without collection errors; missing provenance remains visible. Full-PR p95 is
29m21s (216 samples), merge p95 is 25m35s (73), and reruns are 3.6%. Selective and
nightly p95 remain unavailable at 13 samples each. The changed reporting
population and elapsed collection window make this a corrected baseline, not
measured performance improvement. FAI-1067 remains open against the unchanged
12-minute p95 and 3% rerun objectives.

The live main ruleset retains independent review, required CI/security checks,
linear history and the protected merge queue. No deployment, Terraform apply,
release publication, reviewer assignment, or live environment-policy change was
performed for this reassessment. The environment findings require owner-selected
reviewer identities; workflow guards alone cannot enforce that credential boundary.

## Previously completed qualification

PRs #805, #807, #808, #814, #815, #816, #819 and #821 merged through the protected
queue. The final integrated source above passed exact-candidate merge validation,
security and native proofs, a fresh Nightly run, main image/recovery/schema
qualification, the public-site image, the complete Nix suite, and the trusted
orchestration producer followed by a warm consumer. The
[final qualification handoff](https://linear.app/flid/document/cicd-measured-decisions-and-final-qualification-3-october-2026-60b17c68a2c9)
contains the immutable receipts. This reassessment does not reopen rejected
compiler-mount persistence or recovery concurrency experiments without new evidence.
