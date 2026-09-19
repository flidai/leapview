#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

image="${LEAPVIEW_TEST_FAI518_CANDIDATE_IMAGE:?exact candidate OCI digest reference is required}"
revision="${LEAPVIEW_TEST_FAI518_CANDIDATE_REVISION:?candidate source revision is required}"
predecessor_image="${LEAPVIEW_TEST_FAI518_PREDECESSOR_IMAGE:?exact predecessor OCI digest reference is required}"
predecessor_revision="${LEAPVIEW_TEST_FAI518_PREDECESSOR_REVISION:?predecessor source revision is required}"
git cat-file -e "$predecessor_revision:internal/platform/postgres/migrations/019_refresh_schedule_notifications.sql"
if git cat-file -e "$predecessor_revision:internal/platform/postgres/migrations/020_release_transition_operation.sql" 2>/dev/null; then
  printf '%s\n' 'predecessor source already contains migration 020' >&2
  exit 1
fi
git show "$predecessor_revision:internal/platform/postgres/migrations/goose.go" |
  rg -q 'CurrentRevision int64 = 19' || {
  printf '%s\n' 'predecessor source does not declare revision 019' >&2
  exit 1
}
git cat-file -e "$revision:internal/platform/postgres/migrations/020_release_transition_operation.sql"
cmp -s \
  <(git show "$revision:internal/platform/postgres/migrations/020_release_transition_operation.sql") \
  internal/platform/postgres/migrations/020_release_transition_operation.sql || {
  printf '%s\n' 'candidate source does not contain the qualified migration 020' >&2
  exit 1
}
evidence_dir="${LEAPVIEW_TEST_FAI518_REAL_ARTIFACT_EVIDENCE_DIR:-$repo_root/.tmp/qualification/ubdr/release-transition-real-artifact}"
mkdir -p "$evidence_dir"
evidence_dir="$(cd "$evidence_dir" && pwd)"
rm -f "$evidence_dir/oci-admission.json" "$evidence_dir/verified-attestation.json" \
  "$evidence_dir/sbom.json" "$evidence_dir/transition-report.json"
mkdir -p "$evidence_dir/predecessor"
rm -f "$evidence_dir/predecessor/oci-admission.json" "$evidence_dir/predecessor/verified-attestation.json" \
  "$evidence_dir/predecessor/sbom.json"
export LEAPVIEW_TEST_FAI518_REAL_ARTIFACT_EVIDENCE_DIR="$evidence_dir"
export LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1

if [[ -z "${GH_TOKEN:-}" ]]; then
  GH_TOKEN="$(gh auth token)"
  export GH_TOKEN
fi

policy='.github/security/container-vulnerability-policy.json'
cp "$policy" "$evidence_dir/container-vulnerability-policy.json"
cp "$policy" "$evidence_dir/predecessor/container-vulnerability-policy.json"
go run ./internal/app/tools/ociadmission \
  --image "$predecessor_image" \
  --repository ghcr.io/flidai/leapview \
  --expected-workflow flidai/leapview/.github/workflows/release.yml \
  --source-revision "$predecessor_revision" \
  --policy "$policy" \
  --platform linux/amd64 \
  --mode live \
  --output "$evidence_dir/predecessor/oci-admission.json"
gh attestation verify "oci://$predecessor_image" \
  --repo flidai/leapview \
  --signer-workflow flidai/leapview/.github/workflows/release.yml \
  --source-digest "$predecessor_revision" \
  --deny-self-hosted-runners \
  --format json > "$evidence_dir/predecessor/verified-attestation.json"
docker buildx imagetools inspect "$predecessor_image" --format '{{ json .SBOM }}' > "$evidence_dir/predecessor/sbom.json"
go run ./internal/app/tools/ociadmission \
  --image "$image" \
  --repository ghcr.io/flidai/leapview \
  --expected-workflow flidai/leapview/.github/workflows/release.yml \
  --source-revision "$revision" \
  --policy "$policy" \
  --platform linux/amd64 \
  --mode live \
  --output "$evidence_dir/oci-admission.json"

gh attestation verify "oci://$image" \
  --repo flidai/leapview \
  --signer-workflow flidai/leapview/.github/workflows/release.yml \
  --source-digest "$revision" \
  --deny-self-hosted-runners \
  --format json > "$evidence_dir/verified-attestation.json"
unset GH_TOKEN
docker buildx imagetools inspect "$image" --format '{{ json .SBOM }}' > "$evidence_dir/sbom.json"

go test -race -tags='duckdb_arrow fai518qualification fai518artifactqualification' \
  ./internal/recoveryset/postgres \
  -run '^TestFAI518RealPredecessorCandidateTransitionQualification$' \
  -count=1 -timeout=30m -v
