# Admission of protected Nix outputs

The Nix candidate workflows retain qualified bytes and signed registry bindings.
Their `releaseAdmission: false` remains a candidate result. The separate protected
`nix-output-admission.yml` workflow can issue a canonical OCI artifact receipt only
after authenticating those original artifacts and completing fresh scans of the
exact archive and registry digest. An artifact receipt does not approve a managed
host transition, public site deployment, or a production adoption decision.

The workflow defaults to standalone site images on both native Linux
architectures. Dispatch it on `main` with the exact output kind and successful
corresponding candidate workflow run ID and attempt. It does not infer the latest attempt,
execute tools from the original source checkout, or rebuild candidate bytes.
The original qualification, publication binding, and final published qualification
artifacts must all remain available and unexpired. If an old binary has a new
vulnerability, build a new candidate from the reviewed fixed source; replaying an
old successful report cannot substitute for fresh scanning.

Each architecture produces
`nix-admission-site-image-RUN-ATTEMPT-ARCH`. The final pair check requires the same
source, version, and release identity and rechecks the bound raw reports before
the workflow becomes a successful handoff producer. A failed architecture makes
the entire producer ineligible. Rejected fresh scans remain in explicitly named
diagnostic artifacts, which carry no admission authority.

The canonical receipt has a closed `nixEvidence` profile. Its original build
source, original signer revision, and current admission verifier revision are
separate identities. It binds the candidate archive, original live registry and
signature proofs, original qualification, fresh runtime and Go coverage, and the
fresh OCI scan. Original signed SPDX bytes are retained separately from fresh
scanner SPDX. All raw reports and exact current policy bytes are hashed into the
authenticated artifact bundle. Conventional Buildx/Trivy receipts retain their
existing canonical bytes and transport contract.

The application path and shared `ociadmission admit-nix` command
require the complete native verifier's exact archive/source/platform proof and
raw report inventory. Missing native build provenance, incomplete dependency
coverage, or unresolved native findings refuse admission. The site workflow does
not borrow application evidence or make an application admission claim.

For an eligible application receipt, the existing private managed handoff adds
`--kind application-image --producer-revision ADMISSION_SHA` to its normal exact
run, attempt, artifact, build-source, platform, image and local-verifier inputs.
It authenticates the successful protected admission workflow independently of
the original build source, checks the artifact ZIP SHA-256, verifies the complete
canonical receipt and evidence inventory, and installs only into existing
root-owned private storage. Site receipts have a distinct architecture marker
and cannot authorize a managed application transition.

OCI version or stable alias promotion remains a separate release policy action.
An existing immutable version tag must never be replaced with different Nix
bytes. Independent adoption checks, site observation, managed host qualification,
and desktop operating-system signing requirements remain with their existing
owners.
