# Kamal trial package retirement — 10 October 2026

## Dependency assessment

The retired package is `ghcr.io/flidai/leapview-site-kamal-trial`. The BI
application uses `ghcr.io/flidai/leapview`; the public website and its production
Kamal deployment use `ghcr.io/flidai/leapview-site`.

The production operator's repository, service-label and workflow admission
checks reject trial images. The retained production rejection test exercises
this boundary. The live website's pre-retirement `/build.json` selected
`ghcr.io/flidai/leapview-site@sha256:024c8cefe45104dccb9ea153db6547a9c0116355653701c286080691559605ec`
at revision `3596b920ec4cb50a7cc8fe4e5ac4643a7dd6224d`; `/readyz` passed.

Organization-wide GitHub code search returned seven references, all in this
repository: the trial publisher/preparer/probe, historical evidence and the
production rejection test. It found no indexed consumer in another repository.
The opt-in [trial PR #751](https://github.com/flidai/leapview/pull/751) merged on
29 September; there was no open PR for its branch, and all 13 trial runs were
complete. The publisher was disabled before archival.

The remaining registry dependencies were historical reproduction:

- `real-site.json`, run `36228605549`, image
  `sha256:8e653a743d65a8d90b2bbf65a620524c4d378f936f7754407c0a9fa09d3d8339`.
- `../kamal-site/evidence/manual-vps.json`, run `36230525487`, second image
  `sha256:b60e9a9c1054b4d8071d655f03852d8032df038be3fe0204a53130377563de0c`.
  That experiment's receipt records restoration of the original production site
  and removal of its trial runtime.
- The final successful trial build, run `36543690675`, image
  `sha256:c10c14ca8cb0f9122b1f67fa30715bea7eb2deccadbe0c45ae570d9219a29121`.

Synthetic lifecycle/storage/recovery fixtures use their private registry and
locally built fixture; they do not consume this GHCR package. Their code and
historical observations are preserved.

## Archive and deletion evidence

The [historical archive release](https://github.com/flidai/leapview/releases/tag/archive-kamal-trial-20261010)
preserves all 151 package versions, 151 manifests, 169 configs/layer blobs and
32 available trial admission artifacts. It includes the original package/tag
inventory, untagged versions and attestation images. Each OCI content digest was
verified during archival. Seven numbered parts reconstruct a 6,319,682,992-byte
archive with SHA256
`309a3076f48e054f38b5e05acf155b2944c6caf60e7b65f3648dc179107a91c8`.
Published asset sizes and GitHub-provided SHA256 digests matched the manifest.

The first archival attempt stopped at the release asset size limit before any
deletion. The successful [archive run](https://github.com/flidai/leapview/actions/runs/38016920968)
split the complete archive into parts of at most 1 GB. The release is a
prerelease, is excluded from latest-release selection, and has a non-server tag.

The [deletion run](https://github.com/flidai/leapview/actions/runs/38017646188)
downloads that published archive, verifies part/combined/content hashes and the
unchanged package version/tag inventory, then prepares the three exact historical
images from the retained OCI layout before issuing the scoped package DELETE.
Its [deletion receipt](retirement-20261010.json) records the actual outcome,
offline preparation identities and preservation of both production package IDs.
The release also retains the original `deletion-receipt.json`.

Deletion completed at 08:09:29 IST on 10 October. Both the authenticated package
API and the public package page returned HTTP 404. Production package IDs
`14099618` (`leapview`) and `14095845` (`leapview-site`) were unchanged. The live
website still served the same production image/revision and passed readiness.
The temporary retirement workflow was disabled and its execution branch removed.

The publisher source and its security-coverage entry are removed; production
publisher/deployment configuration is unchanged. The preparer supports
`--archive-layout`, and the [fixture README](../README.md#reproduce-historical-real-image-qualification)
documents reproduction without contacting the retired package. Historical image
references remain in evidence and negative tests intentionally.
