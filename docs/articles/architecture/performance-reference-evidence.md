# Retained performance reference evidence

Production-image qualification remains an absolute-budget smoke until an
independently accepted reference and a paired comparison exist. A successful
image or Actions run alone does not identify the performance report bytes.

The main artifacts workflow retains only `performance-report.json` and
`performance-identity.json` in `performance-evidence-RUN-ATTEMPT`, for 90 days.
The identity records the exact admitted image and source revision. Reports are
retained on failure too, but only a completed successful main artifacts push
with a successful production-image qualification job can establish a reference.
The surrounding evidence directory must never be uploaded: it contains
credentials, instance state and private logs.

After such a run, propose its artifact without changing an accepted reference:

```sh
nix develop --no-update-lock-file -c node scripts/qualify_performance_reference.mjs \
  propose ARTIFACT_ID proposal.json
```

The proposal records the run/attempt/source, immutable image, GitHub artifact ID and
SHA-256, and SHA-256 of the actual retained report. The command downloads the
archive from that repository, checks its upstream digest and size, and accepts
exactly the two named files. It rejects expired artifacts, substituted bytes,
incorrect run/source/image identity and unsuccessful reports. It cannot write
the accepted `.quality/performance-reference.json` and does not invent approval.

Acceptance is an explicit PR adding or changing that reference, with the
calibration and qualification evidence attached. GitHub's normal branch-protection
rules govern PR approval and merging. The required CI gate validates immutable
reference evidence using helpers from trusted main; it does not query reviews or
require a new approval after each commit. Ordinary checking does not promote or
overwrite references. Candidate enforcement edits, renames, build inputs and
policy changes follow the same normal PR approval rules.

To admit an accepted reference for a future candidate:

```sh
nix develop --no-update-lock-file -c node scripts/qualify_performance_reference.mjs \
  check .quality/performance-reference.json PRIVATE_OUTPUT_DIR \
  CANDIDATE_SOURCE_SHA ghcr.io/flidai/leapview@sha256:CANDIDATE_IMAGE_DIGEST
```

The output is the exact admitted report and a bounded admission receipt. The
command rejects using the candidate's own source or image as its reference.
The caller must supply candidate identity from its independently admitted image
and source. This is reference admission, not a timing comparison: serial paired
measurements on observed controlled resources, protocol compatibility,
calibration-backed resource variance rules and independently reviewed frontend
budgets remain separate requirements. No numerical ceiling is introduced by
this plumbing. Expired evidence must be replaced through a new reviewed proposal;
it cannot silently fall back to local files or absolute-only success.
The artifact attempt must match the latest successful run attempt. A failed
earlier attempt cannot inherit a successful retry's job result; after a retry,
the new attempt's retained artifact requires a new reviewed proposal.

Once an accepted reference exists, production-image qualification admits its
retained bytes, measures the reference image first on the current runner, and
then measures the candidate with `QUALIFICATION_PERFORMANCE_BASELINE` pointing
to that fresh reference report. Both images use the same current controller,
fixture and policy. The historical retained report establishes acceptance and
image identity; it is not relabeled as a fresh paired measurement. A failed
reference or comparison stops qualification. Without an accepted reference,
the mode receipt explicitly records `absolute-only-bootstrap` and no comparison.
This is one serial qualification pair using the maintained tolerance, not a
three-pair optimization screen, variance calibration or approval of new budgets.
The fresh reference report, comparison protocol, selected build-input manifest
and comparison-mode receipt are retained for 90 days in a separate bounded
comparison artifact. The accepted-reference artifact keeps its original two-file
format. Private working directories, credentials and logs are not uploaded.
Both journeys use the maintained owned qualification temporary directory.
