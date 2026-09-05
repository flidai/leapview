#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EXAMPLE="$ROOT/examples/dbt-warehouse-boundary"
DBT_PROJECT="$EXAMPLE/dbt"
PUBLISHED="$EXAMPLE/published"
VENV="$ROOT/.tmp/dbt-warehouse-boundary/venv"
REQUIREMENTS="$DBT_PROJECT/requirements.txt"

prepare_dbt() {
  if [[ -n "${DBT_BIN:-}" ]]; then
    [[ -x "$DBT_BIN" ]] || {
      echo "DBT_BIN is not executable" >&2
      return 1
    }
    printf '%s\n' "$DBT_BIN"
    return 0
  fi

  command -v python3 >/dev/null 2>&1 || {
    echo "Python 3 is required for the dbt warehouse-boundary showcase" >&2
    return 1
  }
  mkdir -p "$(dirname "$VENV")"
  local requirements_digest marker
  requirements_digest="$(sha256sum "$REQUIREMENTS" | awk '{print $1}')"
  marker="$VENV/.leapview-requirements.sha256"
  if [[ ! -x "$VENV/bin/dbt" || ! -f "$marker" || "$(<"$marker")" != "$requirements_digest" ]]; then
    python3 -m venv --clear "$VENV"
    "$VENV/bin/python" -m pip install --disable-pip-version-check --requirement "$REQUIREMENTS"
    printf '%s\n' "$requirements_digest" > "$marker"
  fi
  printf '%s\n' "$VENV/bin/dbt"
}

verify_outputs() {
  local expected actual
  expected=$'dim_customers.parquet\nfct_orders.parquet'
  actual="$(find "$PUBLISHED" -maxdepth 1 -type f -name '*.parquet' -printf '%f\n' | LC_ALL=C sort)"
  if [[ "$actual" != "$expected" ]]; then
    echo "dbt publication does not contain the exact expected Parquet file set" >&2
    printf 'expected:\n%s\nactual:\n%s\n' "$expected" "$actual" >&2
    return 1
  fi
  while IFS= read -r file; do
    [[ -s "$PUBLISHED/$file" ]] || {
      echo "dbt publication contains an empty Parquet file: $file" >&2
      return 1
    }
  done <<< "$expected"
  echo "Verified complete dbt publication: dim_customers.parquet, fct_orders.parquet"
}

build() {
  local dbt
  dbt="$(prepare_dbt)"
  mkdir -p "$PUBLISHED"
  # dbt refuses to clean outside its project root. Remove only this example's
  # two declared generated outputs; verify_outputs rejects any unexpected file.
  rm -f "$PUBLISHED/dim_customers.parquet" "$PUBLISHED/fct_orders.parquet"
  (
    cd "$DBT_PROJECT"
    "$dbt" --quiet --no-use-colors build --profiles-dir . --project-dir .
  )
  verify_outputs
}

case "${1:-}" in
  build) build ;;
  verify) verify_outputs ;;
  *) echo "Usage: $0 build|verify" >&2; exit 2 ;;
esac
