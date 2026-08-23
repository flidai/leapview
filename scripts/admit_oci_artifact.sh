#!/usr/bin/env bash
# Fail-closed admission for OCI artifacts crossing a repository boundary.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: admit_oci_artifact.sh --image REPOSITORY@sha256:DIGEST
  --repository OCI_REPOSITORY
  --expected-workflow OWNER/REPO/.github/workflows/WORKFLOW.yml
  --source-revision HEX_SHA
  --policy PATH
  [--mode live|hermetic] [--evidence PATH] [--output PATH]
EOF
  exit 64
}

image=""
oci_repository=""
expected_workflow=""
source_revision=""
policy_path=""
mode="live"
evidence_path=""
output_path=""

while (($#)); do
  case "$1" in
    --image) image="${2:?missing value for --image}"; shift 2 ;;
    --repository) oci_repository="${2:?missing value for --repository}"; shift 2 ;;
    --expected-workflow) expected_workflow="${2:?missing value for --expected-workflow}"; shift 2 ;;
    --source-revision) source_revision="${2:?missing value for --source-revision}"; shift 2 ;;
    --policy) policy_path="${2:?missing value for --policy}"; shift 2 ;;
    --mode) mode="${2:?missing value for --mode}"; shift 2 ;;
    --evidence) evidence_path="${2:?missing value for --evidence}"; shift 2 ;;
    --output) output_path="${2:?missing value for --output}"; shift 2 ;;
    -h|--help) usage ;;
    *) printf 'unknown option: %s\n' "$1" >&2; usage ;;
  esac
done

die() {
  printf 'OCI admission rejected: %s\n' "$*" >&2
  exit 1
}

[[ "$mode" == live || "$mode" == hermetic ]] || die "mode must be live or hermetic"
[[ -n "$image" && -n "$oci_repository" && -n "$expected_workflow" && -n "$source_revision" && -n "$policy_path" ]] || usage
[[ "$oci_repository" =~ ^[a-z0-9]+([./_-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*$ ]] || die "OCI repository is invalid"
[[ "$image" == "$oci_repository@sha256:"* ]] || die "image must use the expected repository and a digest"
[[ "$image" =~ ^[a-z0-9]+([./_-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*@sha256:[0-9a-f]{64}$ ]] || die "image must be repository@sha256:<64 lowercase hex>"
[[ "$expected_workflow" == flidai/leapview/.github/workflows/*.yml ]] || die "workflow identity is outside flidai/leapview"
[[ "$source_revision" =~ ^[0-9a-f]{40}$ ]] || die "source revision must be a full commit SHA"
[[ -f "$policy_path" ]] || die "vulnerability policy is missing"

if ! command -v jq >/dev/null 2>&1; then
  die "jq verifier is required"
fi

policy_json="$(jq -cS . "$policy_path")" || die "vulnerability policy is not valid JSON"
jq -e '
  . as $policy |
  .schemaVersion == 1 and
  .scanner == "trivy" and
  (.scannerVersion | type == "string" and test("^[0-9]+\\.[0-9]+\\.[0-9]+$")) and
  ($policy.scannerImage | test("^aquasec/trivy:" + $policy.scannerVersion + "@sha256:[0-9a-f]{64}$")) and
  (.severity | type == "array" and length > 0 and all(.[]; . == "CRITICAL" or . == "HIGH" or . == "MEDIUM" or . == "LOW")) and
  (.ignoreUnfixed | type == "boolean") and
  (.maxUnresolved | type == "number" and . >= 0 and floor == .)
' <<<"$policy_json" >/dev/null || die "vulnerability policy is not pinned"

digest="${image##*@}"
write_output() {
  local result="$1"
  if [[ -n "$output_path" ]]; then
    mkdir -p "$(dirname "$output_path")"
    printf '%s\n' "$result" > "$output_path"
  fi
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    printf 'image=%s\n' "$image" >> "$GITHUB_OUTPUT"
    printf 'digest=%s\n' "$digest" >> "$GITHUB_OUTPUT"
  fi
  printf '%s\n' "$image"
}

if [[ "$mode" == hermetic ]]; then
  [[ -n "$evidence_path" && -f "$evidence_path" ]] || die "hermetic mode requires evidence"
  evidence_json="$(jq -cS . "$evidence_path")" || die "hermetic evidence is not valid JSON"
  jq -e \
    --arg image "$image" \
    --arg repository "flidai/leapview" \
    --arg workflow "$expected_workflow" \
    --arg revision "$source_revision" \
    --arg digest "$digest" \
    --arg policy_sha256 "$(sha256sum "$policy_path" | awk '{print $1}')" \
    '
      .schemaVersion == 1 and
      .image == $image and
      .digest == $digest and
      .registryDigest == $digest and
      .attestation.verified == true and
      .attestation.repository == $repository and
      .attestation.workflow == $workflow and
      .attestation.sourceRevision == $revision and
      .sbom.discoverable == true and
      .sbom.predicateType == "https://spdx.dev/Document/v2.3" and
      .vulnerabilityPolicy.sha256 == $policy_sha256 and
      .vulnerabilityPolicy.scanner == "trivy" and
      .vulnerabilityPolicy.passed == true
    ' <<<"$evidence_json" >/dev/null || die "hermetic evidence is missing verified identity, SBOM, digest, or policy"
  write_output "$evidence_json"
  exit 0
fi

for verifier in gh docker; do
  command -v "$verifier" >/dev/null 2>&1 || die "live verifier $verifier is missing"
done
[[ -n "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ]] || die "live verification requires GH_TOKEN or GITHUB_TOKEN"
gh attestation verify --help >/dev/null 2>&1 || die "live verifier gh attestation is missing"

github_repository="${GITHUB_REPOSITORY:-flidai/leapview}"
[[ "$github_repository" == flidai/leapview ]] || die "GitHub repository identity is not flidai/leapview"

attestation_json="$(mktemp)"
registry_json="$(mktemp)"
trivy_json="$(mktemp)"
trap 'rm -f "$attestation_json" "$registry_json" "$trivy_json"' EXIT

GH_TOKEN="${GH_TOKEN:-${GITHUB_TOKEN}}" gh attestation verify "oci://$image" \
  --repo flidai/leapview \
  --signer-workflow "$expected_workflow" \
  --source-digest "$source_revision" \
  --deny-self-hosted-runners \
  --format json > "$attestation_json" || die "GitHub provenance attestation did not verify"

jq -e \
  --arg workflow "$expected_workflow" \
  --arg revision "$source_revision" \
  'length > 0 and any(.[];
    (.verificationResult.signature.certificate.sourceRepository == "flidai/leapview") and
    ((.verificationResult.signature.certificate.workflow // .verificationResult.signature.certificate.workflowPath // .verificationResult.signature.certificate.buildConfigURI // .verificationResult.signature.certificate.subjectAlternativeName // "") | contains($workflow)) and
    ((.verificationResult.signature.certificate.sourceRepositoryDigest // .verificationResult.signature.certificate.sourceDigest // "") == $revision)
  )' "$attestation_json" >/dev/null || die "attestation identity or source revision is wrong"

# BuildKit publishes SPDX beside the exact manifest digest. Ask buildx to
# resolve and decode that attestation, then validate the document identity.
docker buildx imagetools inspect "$image" \
  --format '{{ json .SBOM }}' > "$registry_json" || die "OCI SBOM could not be inspected"
jq -e '
  (.SPDX.SPDXID == "SPDXRef-DOCUMENT") or
  ([.. | objects | select(.SPDXID? == "SPDXRef-DOCUMENT")] | length > 0)
' "$registry_json" >/dev/null || die "no SPDX SBOM was discoverable for this digest"

scanner_version="$(jq -r '.scannerVersion' <<<"$policy_json")"
scanner_image="$(jq -r '.scannerImage' <<<"$policy_json")"
if command -v trivy >/dev/null 2>&1; then
  trivy_command=(trivy)
else
  command -v docker >/dev/null 2>&1 || die "pinned trivy verifier is missing"
  docker info >/dev/null 2>&1 || die "pinned trivy verifier cannot access Docker"
  trivy_command=(docker run --rm --network host
    -v /var/run/docker.sock:/var/run/docker.sock
    -v "${HOME:-/root}/.docker:/root/.docker:ro"
    "$scanner_image")
fi
actual_scanner_version="$("${trivy_command[@]}" version --format json | jq -r '.Version // .version // empty')" || die "could not determine trivy version"
[[ "$actual_scanner_version" == "$scanner_version" ]] || die "trivy $actual_scanner_version does not match pinned $scanner_version"
severity_args=()
while IFS= read -r severity; do severity_args+=(--severity "$severity"); done < <(jq -r '.severity[]' <<<"$policy_json")
ignore_args=()
if [[ "$(jq -r '.ignoreUnfixed' <<<"$policy_json")" == true ]]; then ignore_args+=(--ignore-unfixed); fi
"${trivy_command[@]}" image --quiet --format json --exit-code 0 "${severity_args[@]}" "${ignore_args[@]}" "$image" > "$trivy_json" || die "pinned vulnerability scan could not complete"
jq -e --argjson max "$(jq '.maxUnresolved' <<<"$policy_json")" '
  ([.Results[]?.Vulnerabilities[]?] | length) <= $max
' "$trivy_json" >/dev/null || die "vulnerability evidence exceeds policy"

write_output "$(jq -n \
  --arg image "$image" \
  --arg digest "$digest" \
  --arg workflow "$expected_workflow" \
  --arg revision "$source_revision" \
  --arg policy_sha256 "$(sha256sum "$policy_path" | awk '{print $1}')" \
  '{schemaVersion:1,image:$image,digest:$digest,registryDigest:$digest,attestation:{verified:true,repository:"flidai/leapview",workflow:$workflow,sourceRevision:$revision},sbom:{discoverable:true,predicateType:"https://spdx.dev/Document/v2.3"},vulnerabilityPolicy:{sha256:$policy_sha256,scanner:"trivy",passed:true}}')"
