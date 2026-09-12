# Current-main builder bundle proposal — not applied

The unchanged frontend policy rejects the dashboard builder shipped by current
main. This archive records an exact, review-only proposal; it does not change a
budget, baseline, relative ratchet, dependency, or shipped feature. The proposal
has `approved: false` and cannot authorize its own application or a merge.

## Identity and method

- Clean measured integration: `1849a112633945472e15c5d51f4b58c560bbd41f`.
- Integrated main: `ab9f8e124deb221e2fb097aee8e6881e6be3f3ea`.
- Toolchain: Bun 1.3.14, Node compatibility v24.3.0, `bun@1.3.14`,
  lockfile SHA-256 `cc857c74a196402e1e4f65175f0daefeaab513c945c351ee07d3cacf1c344507`.
- Environment: local shared Debian host, Linux 6.12.105+deb13-amd64, x86_64,
  16 QEMU Virtual CPU version 2.5+ logical CPUs. Bundle bytes are not runtime
  performance measurements.
- Three sequential `bun run build` invocations each cleaned emitted production
  outputs and produced byte-identical reports. Dependencies were reused; these
  are clean-output reproducibility runs, not independent installations.
- [normal.json](normal.json) SHA-256:
  `589ae26caab58795205a2a61ae7b5c7c277c5fc0894e0bfb4334b9edadfe69ff`.
- [proposal.json](proposal.json) SHA-256:
  `9ddd190474e32b82120200bd4b724372c1e603953d25805939c7ccb60cd80f43`.
- [reproducibility.json](reproducibility.json) records all three report hashes.

## Result and attribution

| Scope | Existing limit raw / gzip | Measured raw / gzip | Result |
| --- | ---: | ---: | --- |
| catalog-page | 555,161 / 120,175 | 499,260 / 115,883 | passes |
| dashboard-builder | 5,780,641 / 1,116,846 | 6,393,756 / 1,463,063 | fails |
| deduplicated aggregate | 15,934,947 / 3,183,940 | 13,306,037 / 3,004,527 | passes |

The builder is over by 613,115 raw bytes (10.61%) and 346,217 gzip bytes
(31.00%). Source and emitted-chunk review attributes the material change since
calibration to interactive dashboard builder commit `4f677d4b9` (#377), which
added GridStack, the dashboard icon picker, and the embedded chat surface with
Markdown, DOMPurify, and Shiki dependencies. Chart-formatting commit `570e2bc5f`
(#533) is not the primary regression: its current ECharts output is smaller than
the calibrated output.

The feature-preserving Lucide metadata/node split in this candidate removes
picker-only metadata from the general icon renderer closure. It makes the
catalog pass its existing limit while retaining every canonical icon and alias.
Identifier/syntax minification experiments were insufficient and were reverted.
No supported chart, chat, icon, or builder feature was removed, and dynamically
loaded files were not hidden from transitive route accounting.

The existing controlled-regression tests prove that oversized, missing,
malformed, duplicate, unexpected, and current-head self-baseline evidence fail
closed. An owner must either approve the exact measured builder increase through
the governed proposal flow or deliver a feature-preserving size reduction.
Before application, regenerate the proposal against the then-current clean head;
GitHub collaborator approval of that exact PR commit and all required checks are
separate requirements. This archive is not merge approval.
