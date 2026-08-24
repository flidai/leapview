# Security policy

LeapView accepts private reports for vulnerabilities in the application,
published OCI images, Desktop artifacts, documentation site, deployment
material, and repository automation. Please do not open a public issue for a
security vulnerability or include credentials, customer data, tokens, or
unredacted logs in a report.

## Report privately

Use a [GitHub private security advisory](https://github.com/flidai/leapview/security/advisories/new)
for this repository. If that channel is unavailable, contact the maintainers
through the support address published on [leapview.dev](https://leapview.dev/) and
request a private security conversation. Include **security report** in the
subject and do not attach secrets. You may also send only the request for a
private channel to `security@leapview.dev`; do not send credentials or exploit
payloads by email. We acknowledge a report within five
business days, provide a triage decision as soon as the impact and affected
versions are understood, and coordinate a fix, credit, and disclosure date
with the reporter.

A useful report contains:

- the affected version, immutable image or Desktop artifact digest, and source
  revision when known;
- a short description of the security boundary and the smallest reproducible
  steps or proof of concept;
- impact, affected configuration, and whether data or credentials may have
  been exposed; and
- privacy-safe logs, timestamps, and links to the relevant advisory or
  provenance evidence.

Do not test against an instance or data set that you do not own or have
explicit permission to assess. Stop testing when you have established impact;
do not copy, alter, delete, or retain customer data.

## Scope and supported versions

The latest published LeapView release and the maintained default branch are
supported. Older releases may be investigated when they are still deployed,
but a fix normally targets the latest release. Hosted demonstration systems
are not a test target; report a problem privately instead.

The disclosure policy covers application authorization and authentication,
query and data-policy enforcement, credential handling, managed-data and
backup boundaries, deployment and release provenance, OCI images, Desktop
remote-content isolation, update channels, and repository automation. It does
not promise a bounty or support unsupported forks, modified binaries, or
intentional denial-of-service testing.

## Release and repository guardrails

The repository security contract is described in [Security governance and
release assurance](/docs/security/governance). In brief:

- the active `main` ruleset requires the exact `CI gate` and `Security gate`
  status contexts;
- scanner and provenance failures fail closed—an unavailable feed, malformed
  result, timeout, missing attestation, or unverifiable artifact is not a
  reason to bypass a required check;
- security exceptions are bounded, approved records with an owner, evidence,
  compensating controls, and an expiry no more than 90 calendar days after
  creation. They do not waive provenance or artifact verification and are not
  silently renewed; and
- production promotion uses immutable digests and independent OCI and
  Desktop verification. A release or provenance incident freezes promotion
  and follows the recovery procedure in the governance guide.

When reporting a release or provenance issue, preserve the immutable digest,
source revision, workflow run, attestations, SBOM, release manifest, and
verification output. Do not replace a tag or delete evidence while an
investigation is open.

## Coordinated disclosure

We will keep the reporter informed about triage, the affected release, and a
safe disclosure window. We may request a private reproduction or a minimal
proof that avoids personal or customer information. We credit reporters when
they want credit and when doing so does not create a safety or privacy risk.

If a report is already public, send the private advisory link immediately so
we can coordinate containment; do not add exploit details to the public issue.
