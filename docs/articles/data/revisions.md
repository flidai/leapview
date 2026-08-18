# Data revisions and activation

Managed data is stored as immutable, content-addressed revisions. A revision digest identifies the canonical manifest of files staged for one project-global managed connection. Staging creates a candidate; deployment decides which candidate an environment serves.

## Plan local input

Plan before uploading:

```sh
leapview data plan \
  --project dashboards/leapview.yaml \
  --connection olist \
  --from /srv/olist
```

Planning discovers the files referenced by the managed connection, hashes them, and prints the canonical manifest. It is local and does not alter server state. Keep the source directory stable while hashing; a changed file belongs to a new plan.

Use `--previous-manifest` when reviewing a later revision and you want files classified as added, changed, removed, or unchanged. The digest represents content and canonical manifest metadata, not a mutable folder name or upload timestamp.

## Stage without activation

Upload missing objects and stage the revision:

```sh
leapview data sync \
  --project dashboards/leapview.yaml \
  --connection olist \
  --from /srv/olist \
  --target "$LEAPVIEW_TARGET" \
  --token "$LEAPVIEW_API_TOKEN"
```

The server deduplicates already available content and returns a lowercase `sha256:` revision digest. Staging is intentionally non-serving: dashboards continue querying the active revision until a project deployment activates the new digest.

Do not modify the source tree during transfer. If content changes, let the operation fail and create a new plan rather than trying to preserve the old digest.

## Bind revisions to a private candidate

After staging every managed connection required by the project, create the
private target candidate:

For a private preview before the reviewed build, run `leapview dev --once` with
the same project and target.

```sh
PLAN_JSON=$(leapview plan dashboards/leapview.yaml --target "$LEAPVIEW_TARGET" --format json)
PLAN_ID=$(printf '%s' "$PLAN_JSON" | jq -r .planId)
BUILD_JSON=$(leapview build "$PLAN_ID" --format json)
CANDIDATE_ID=$(printf '%s' "$BUILD_JSON" | jq -r .candidateId)
leapview publish "$CANDIDATE_ID"
```

Build resolves the exact staged revision for every managed connection and
retains those pins in immutable provenance. Missing,
incompatible, or unavailable revisions reject the candidate. After review,
`publish` activates that exact candidate without rebuilding it. Successful
activation moves project configuration, project serving state, and managed
revision pointers as one reviewed rollout.

This coupling matters when model SQL changes alongside input data. Activating only the file or only the model could create an incompatible serving state; the project deployment makes their intended combination explicit.

## Inspect staged and active state

Server revision commands use the project resource ID, not the local manifest path:

```sh
leapview data revisions list \
  --project leapview-showcase \
  --connection olist \
  --target "$LEAPVIEW_TARGET" \
  --token "$LEAPVIEW_API_TOKEN"

leapview data revisions current \
  --project leapview-showcase \
  --connection olist \
  --environment prod \
  --target "$LEAPVIEW_TARGET" \
  --token "$LEAPVIEW_API_TOKEN"
```

Listing shows staged revisions with pagination controls. `current` reports the environment's active digest or `none`. Record the reviewed digest in deployment logs so an operator can distinguish the intended rollout from another staged candidate.

## Retry and rollback boundaries

Revision identity is immutable; activation is mutable. Re-uploading the same canonical content can safely resolve to the same digest, while changed content produces a new digest. A failed deployment leaves prior active pointers unchanged.

A later deployment can reselect a previously staged revision only if the surrounding project and retention policy still make it a valid candidate. Do not assume every historical analytical snapshot is a customer-facing rollback point; managed source revisions and DuckLake serving snapshots have different lifecycles.

See [Managed data ingestion](/docs/data-ingestion) and the generated [`data revisions` reference](/docs/cli/data-revisions) for exact flags.
