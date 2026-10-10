# Requalification after main integration — 2026-10-10

Main `9ddbeb52b86e42f4f4456f51d7d752ced495124c` supplies Go 1.27.2, x/net v0.60.0 and the corresponding patched Nix toolchain. This separate check uses a newly compiled real Go helper against the merged source; it does not rerun author inference or modify the original 90-trial report.

All 93 fixed prototype/fixture cases retain their expected results. All 90 preserved authored outputs pass structural decoding, actual project compilation and exact intended behavior, with the same canonical source hashes and outcomes as the original final grades. All 108 authored file hashes and 55 frozen corpus input hashes remain unchanged. The reports record zero new agent trials and no query execution.

`identity.json` records exact input and build identity. `requalify.ts.txt` preserves the check without entering test discovery. The helper and lowered source roots remain at `/var/tmp/pr938-oct10-requalification`; the helper was built from the merged working tree before the merge commit. Its SHA-256 is `8553430c1794376d8d8b77abeaf09d34cca056abc3f16f61bdc94631064272ec`.
