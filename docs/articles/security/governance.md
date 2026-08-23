# Security governance and release assurance

This page is the operational contract for repository security. It explains who
owns scanner findings, how a bounded exception is reviewed, what happens when
a feed or tool is unavailable, and how to verify the exact OCI or Desktop
artifact that may be promoted. The contract applies to `flidai/leapview` and
is intentionally auditable from a read-only GitHub API snapshot.

## Private disclosure and triage

Use the [private security advisory](https://github.com/flidai/leapview/security/advisories/new)
for a vulnerability. Do not put exploit details, credentials, customer data,
or unredacted diagnostics in a public issue. See the repository [security
policy](https://github.com/flidai/leapview/blob/main/SECURITY.md) for scope,
report contents, and coordinated disclosure.

The security owner acknowledges reports within five business days and records
an initial severity, affected component and release, exploitability, exposure
status, and an accountable remediation owner. Triage is a private record even
when the eventual fix is public. The release owner, platform owner, and
component owner are consulted when a finding crosses their boundaries.

## Scanner ownership and required checks

`Security gates` is the required workflow for every pull request and merge
queue candidate. `Nightly CI / Nightly dependency security` provides the
scheduled broad scan and catches drift between changes. Its lanes have stable
ownership and names:

| Lane | Scope | Primary owner |
| --- | --- | --- |
| `Security gates / Security policy contracts` | `.security` inventory, validated exceptions, and updater coverage | Security/platform owner |
| `Security gates / Dependency vulnerability policy` | Every maintained Go, JavaScript, Terraform, image, and action surface | Component owners, coordinated by the security owner |
| `Security gates / Secret and IaC policy` | Repository-history secrets plus pinned Trivy secret/misconfiguration scans | Platform/release owner |
| `Security gates / Selected SAST (go)` and `(javascript-typescript)` | Selected CodeQL analysis | Go and frontend owners |
| `Security gates / Security gate` | Requires every security lane to pass | Security owner |

The dependency lane runs pinned `govulncheck` for every maintained Go module,
`bun audit` for every Bun lockfile, and `npm audit` for every npm lockfile. The
source lane runs Gitleaks over the current tree and candidate history, then
uses pinned Trivy secret and misconfiguration scanning for Terraform,
Dockerfile, and GitHub Actions surfaces. The policy lane independently rejects
unpinned third-party actions. The repository's coverage inventory is the
source of truth for which scanner applies to each surface. The direct package
commands are:

| Scan | Command | Primary owner | Evidence |
| --- | --- | --- | --- |
| JavaScript dependency audit | `task security:dependencies` → `bun audit` / `npm audit` | Frontend owner | Every declared lockfile and audit output |
| Go dependency and call-path audit | `task security:dependencies` → `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...` | Go/platform owner | Every declared Go module and govulncheck output |

JavaScript audits block Critical findings. High findings remain visible in the
bounded audit output and are triaged by the component owner; this distinction
keeps unshipped build-tool advisories visible without equating them to
reachable runtime vulnerabilities. `govulncheck` blocks every reachable Go
finding regardless of advisory severity. Source/IaC and candidate-image scans
block High and Critical findings. Changes to these thresholds require the same
review as changes to the required workflow.

The security owner triages both feeds, assigns each accepted finding to the
component owner, and verifies the remediation or exception evidence. The
platform owner maintains the workflow, ruleset, runner permissions, and
environment protections. The release owner is accountable for the exact
candidate digest, attestation, SBOM, and final promotion evidence. No owner
may approve their own exception without a second security approver.

The active ruleset is named `main` and protects `refs/heads/main`. Its required
status contexts are exactly:

```text
CI gate
Security gate
```

These are GitHub status contexts, not a suggestion to accept a similarly named
job. A missing, renamed, skipped, stale, or inconclusive context fails the
gate. Lower layers of a pull-request stack may defer ordinary CI to the stack
tip, but the merge queue must evaluate the exact candidate selected for main.

## Validated exceptions

An exception is a temporary risk decision for a known finding; it is not a
scanner disable switch. The security owner validates a record before the
`Security gate` can account for it. Every machine-readable record in
`.security/exceptions.yaml` must contain these fields and no renamed variants:

| Field | Requirement |
| --- | --- |
| `id` | Stable unique identifier; never reused |
| `scanner` | Exact scanner name from the covered surface (for example `osv-scanner` or `govulncheck`) |
| `rule` | CVE, GHSA, OSV, or scanner rule identifier |
| `resource` | One narrow package, module, image layer, or source path |
| `owner` | Named individual or team accountable for removal |
| `rationale` | Why the finding is not currently exploitable or cannot yet be fixed; include links to the advisory, severity, and compensating-control review here |
| `created` | UTC date of approval |
| `expires` | UTC date no later than `created + 90 calendar days` |

Advisory/component aliases, severity, concrete compensating controls,
immutable scanner evidence, lifecycle status, and both approver identities are
review context, not additional machine-field names. Record that context in the
linked private finding or review record and reference it from `rationale`.
The validator rejects unknown YAML fields so an exception cannot silently add a
broader waiver.

The workflow is: reproduce the scanner result; assess exploitability and
affected release paths; propose the record and controls; obtain two-person
approval; validate all fields and the 90-day maximum; attach the immutable
evidence to the private finding; and schedule remediation before expiry. An
expired, incomplete, ownerless, unapproved, or evidence-free record is a gate
failure. There is no automatic extension: renewal requires a new review,
updated evidence, and a new maximum of 90 days. High- and critical-risk
findings, release-signing findings, and provenance findings cannot be waived by
this process.

## Fail closed on feed or tool outage

Scanner feeds and tools are security inputs. An outage, rate limit, timeout,
invalid signature, malformed output, missing generated inputs, or unknown
scanner status produces a failed `Security gate`. The workflow must not
convert an outage to “no findings,” reuse an old pass, or create an emergency
exception. Retry the same reviewed commit after restoring the scanner or use a
separately approved maintenance window that still leaves promotion blocked.

The same rule applies to provenance and SBOM verification: a missing or
unverifiable attestation is a failed candidate, even when the image starts and
the checksum is known. Preserve the failure output and feed/tool version in
the incident record; do not hand-edit a report to make a gate pass.

## Trusted builders and governed environments

The following workflow files are the only repository-declared builders for
these artifact classes. Compare both the path and workflow display name when
reviewing provenance:

| Artifact class | Trusted workflow path | Workflow name |
| --- | --- | --- |
| Main OCI image | `.github/workflows/artifacts.yml` | `Main artifacts` |
| Release OCI image | `.github/workflows/release.yml` | `Release image` |
| Public-site OCI image | `.github/workflows/site-image.yml` | `Publish public site image` |
| Desktop security evidence | `.github/workflows/electron-security-proof.yml` | `Electron security proof` |
| Unsigned Desktop preview | `.github/workflows/desktop-preview-release.yml` | `Desktop unsigned preview release` |

The attestation identity is the repository-qualified workflow path
`flidai/leapview/.github/workflows/<path>` at the reviewed ref. A similarly
named workflow in another repository, a fork, or an unreviewed ref is not a
trusted builder.

The governed deployment environments are:

| Environment | Use | Required boundary |
| --- | --- | --- |
| `leapview-demo` | Hosted Olist demonstration | Protected main branch and human review; deploy only an immutable qualified image |
| `leapview-ephemeral-qualification` | Disposable Hetzner qualification | Human review; manual dispatch is restricted to the explicitly named workflow and an immutable attested image; destroy after the run |
| `leapview-site-production` | Public-site infrastructure and promotion | Protected main branch, human review, immutable image promotion, and post-activation health verification |

The `desktop-preview` environment is an unsigned evaluation publication and
does not authorize production signing or deployment. Environment settings must
not expose secrets to pull requests or unreviewed refs. Changes to reviewers,
branch policy, deployment credentials, signing identity, or environment
secrets require two-person review and an audit record.

## Provenance incident recovery

Treat a provenance mismatch, unexpected builder, missing attestation, SBOM
drift, signing compromise, or digest/tag mismatch as a release incident:

1. Stop promotion and freeze the affected OCI tag, Desktop channel, and
   updater/publication pointer. Do not overwrite or delete immutable objects.
2. Quarantine the digest and preserve the source revision, workflow run,
   builder identity, attestation bundle, SBOM, release manifest, checksums,
   logs, and verification output.
3. Revoke or rotate the suspected credential, signing key, deployment token, or
   environment secret. Review access and issue a new identity rather than
   trusting a repaired old one.
4. Determine the last known-good immutable release and keep it available only
   when its independent verification still passes. Withdraw affected downloads
   and updater eligibility without silently downgrading clients.
5. Rebuild from a reviewed `main` commit through a trusted builder, regenerate
   the SBOM and provenance, and run the full qualification path. Never repair an
   existing release by replacing bytes in place.
6. Have an independent assessor verify the new OCI and Desktop artifacts from
   outside the publishing session, then record the incident decision and
   follow-up controls before unfreezing a channel.

## Read-only governance audit

Capture detailed GitHub API responses without changing repository settings.
The list endpoint is only a summary, so resolve the `main` ruleset ID first:

```sh
ruleset_id=$(gh api --method GET repos/flidai/leapview/rulesets --jq '.[] | select(.name == "main") | .id')
gh api --method GET "repos/flidai/leapview/rulesets/$ruleset_id" > main-ruleset.json
for environment in leapview-demo leapview-ephemeral-qualification leapview-site-production; do
  gh api --method GET "repos/flidai/leapview/environments/$environment" > "$environment.json"
done
jq -n --slurpfile ruleset main-ruleset.json \
  --slurpfile demo leapview-demo.json \
  --slurpfile qualification leapview-ephemeral-qualification.json \
  --slurpfile site leapview-site-production.json \
  '{rulesets: $ruleset, environments: [$demo[0], $qualification[0], $site[0]]}' > governance.json
```

Run the offline checker against that injected snapshot:

```sh
go run ./internal/app/tools/governanceaudit --snapshot governance.json --json
```

The checker reports every drift and exits non-zero on a missing or malformed
section, an inactive or mis-scoped `main` ruleset, either required status
context, a governed environment, a reviewer rule, or a protected-main branch
policy. `--live --repo flidai/leapview` is an optional convenience that performs
only detailed GET requests through `gh`; the default mode never makes a
network request. It has no setting mutation or delete operation.

## Independent artifact verification

### OCI

Use the digest emitted by the build, not a mutable tag. An independent verifier
should inspect the manifest and labels, then verify GitHub's attestation before
running the image:

```sh
image=ghcr.io/flidai/leapview@sha256:<64-hex-digest>
docker buildx imagetools inspect "$image"
docker buildx imagetools inspect "$image" --format '{{ json .SBOM }}' | jq -e '.. | objects | select(.SPDXID? == "SPDXRef-DOCUMENT")'
gh attestation verify "oci://$image" --repo flidai/leapview
docker pull "$image"
docker image inspect "$image" --format '{{json .Config.Labels}}'
docker run --rm "$image" version --json
```

The attestation must name one of the trusted builder workflows above, bind the
reviewed source revision and exact digest, and include an SBOM. A successful
container start is not evidence of provenance.

### Desktop

Download the exact artifact, checksum document, SPDX SBOM, release manifest,
and provenance from the immutable release location. Follow [Verify a desktop
release](/docs/desktop/release-verification) and independently check the
platform-native signature: `spctl`/`codesign` on macOS, Authenticode status on
Windows, and signed APT metadata on Ubuntu. Confirm that the artifact,
updater companions, manifest, SBOM, provenance, source revision, architecture,
and digest agree. The `Electron security proof / Electron gate` result is
required for merge-queue candidates; a JSON declaration alone is never a
signature or provenance proof.

Record the commands, tool versions, digest, and bounded output with the
release evidence. Never upload credentials, cookies, customer origins, query
results, or unredacted native error text.
