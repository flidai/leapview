# Public-site live acceptance — 2 October 2026

All required live proofs passed for the manually operated Caddy/Kamal website.
The replacement observation covered a full 24 hours, both adoption boundary
smokes passed, and the frozen acceptance and independent final read-only audit
passed. Fallback PR #748 was closed externally as superseded at 16:07:20 IST
on 2 October, citing the reviewable evidence in #803. Final documentation review and protected merge completed in
[PR #803](https://github.com/flidai/leapview/pull/803), commit
`c7b16569b9e9da1331d1c2fbb27e1bbb91046984`, at 12:24:33 UTC on 2 October. Automatic VPS activation remains deferred; the demo
was outside scope.

The [sanitized receipt index](final-acceptance-20261002.json) binds these results
to protected source receipts by SHA-256. It publishes no operator locations,
access material, host identifiers or raw probe output. The historical
[30 September rollout](live-rollout-20260930.md) and
[1 October recovery snapshot](observation-recovery-20261001.json) remain separate.
The interrupted September 30 interval and the earlier failed v3 monitor are
excluded; their samples do not contribute to the accepted interval.

| Proof | Verified result |
| --- | --- |
| Replacement interval | 1 October 15:14:34 IST through 2 October 15:14:36 IST; 86,402.475 elapsed seconds for the required 86,400 seconds |
| Public health, readiness and exact build identity | 1,441 minute samples, three checks each; no failed or rejected samples |
| Host/storage observation | 97 checks at 15-minute intervals; stable topology, boot identity, container identities and restart counts |
| Boundary adoption | Separate start and end smokes passed: release/build metadata, documentation, assets, advertised download/checksum links and redirects |
| Observer termination | Supervised worker exit 0; supported CLI exit 0; exact final summary, process, supervisor and worker-exit hashes bound to the outer receipt |
| Observer output | Independently inspected; no I/O failure or rejected-run diagnostic |
| Frozen acceptance | Receipt passed and actual wrapper exit 0 at 15:17:08 IST on 2 October; config, source, bundle, receipt, run and outer-exit bindings verified |
| Final read-only audit | Completed at 15:26:51 IST on 2 October; independent review passed |

HTTP success and a receipt status alone were not accepted as proof. Independent
inspection checked the complete raw sequence, every host check against the frozen
validator, all source/input hashes, both observer exits and the acceptance wrapper
bindings. The dispatcher record was still in flight during this audit and was
not used as proof that closeout had completed.

| Role | Immutable image digest | Service source revision |
| --- | --- | --- |
| Active B | `sha256:acc225d4526738b4a7b55609b1a643e31fdbc034dca305f17ca952a973f16e87` | `0f25215ceb1cc7331701d2f3120da47b66a88714` |
| Retained A | `sha256:87a5b2068742b8c216dbfbcc578d93789e4f6db1ca771f44834f654b54de6eec` | `aa823506bf7916f051083cab56dd015cbcadac0d` |

The audit verified both local OCI indexes, admitted Linux amd64 manifests,
configs and all 14 layer blobs per image by content hash and descriptor size.
Docker's selected platform descriptor and filesystem-layer identities matched
the admitted records. B remains active and A remains the distinct locally
recoverable prior. No image pull, deployment, pruning, rollback, reboot or
production configuration change occurred during this observation/closeout.

The observation's minimum free capacity was 33,355,149,312 bytes and 2,398,341
inodes. The final audit measured 33,355,091,968 bytes and 2,398,341 inodes on the
single shared Docker/containerd filesystem, exceeding the unchanged required
5,900,624,282 bytes and 13,787 inodes. No host drift, unexpected restart,
unresolved operator ownership, pending operation or pending maintenance was
found. The retired updater timer remained inactive and disabled; its static service
remained inactive.

On-host verification matched the protected original backup manifest, six
original configuration/entrypoint files and four saved inspection snapshots,
including protected ownership and modes. Local original recovery and Caddy-data
archives, the backup receipt and capture source matched their frozen checksums.
The audit inspected recovery material without restoring it. The original
Compose restoration, offline A/B recovery, Caddy recreation and real reboot
proofs remain the earlier recorded live results; these destructive drills were
not repeated during closeout.

The merged replacements include the [manual operator #752](https://github.com/flidai/leapview/pull/752),
[qualification #751](https://github.com/flidai/leapview/pull/751),
[rollout evidence #781](https://github.com/flidai/leapview/pull/781),
[portable gates #782](https://github.com/flidai/leapview/pull/782) and
[observer recovery #790](https://github.com/flidai/leapview/pull/790).
PR #790 passed [exact-head merge validation](https://github.com/flidai/leapview/actions/runs/36847866098),
[security](https://github.com/flidai/leapview/actions/runs/36847865869) and
[Electron](https://github.com/flidai/leapview/actions/runs/36847865864) before merging.
The final documentation passed its normal checks and approving review and merged
in #803 on 2 October 2026. The [fallback closure comment](https://github.com/flidai/leapview/pull/748#issuecomment-5950542382)
records its external retirement after all required live proofs passed and the
acceptance evidence became reviewable in [#803](https://github.com/flidai/leapview/pull/803).
This task did not close or install the fallback. Manual live acceptance does not
complete automatic CI/CD activation.
