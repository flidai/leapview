# Pagestream upstream provenance

This directory is LeapView's in-repository fork of the Pagestream Go module.
The package originated in LeapView, was temporarily published from Toolbelt,
and is now product-owned again so its surface can stay aligned with LeapView's
stream-first MPA architecture.

- Source repository: https://github.com/Yacobolo/toolbelt
- Module: `github.com/Yacobolo/toolbelt/pagestream`
- Imported version: `v0.0.0-20260802184245-b486599808d1`
- Upstream commit: `b486599808d1`
- Imported from: the Go module cache
- Import date: 2026-08-24

The initial import matched that version. LeapView then removed the upstream
trace store, signal-history snapshots, client-cookie policy, delivery envelopes,
generation-aware coalescing, and their stream hooks. Tests use LeapView's
existing SSE test helper rather than copying the upstream module's internal
helper into the product package.

## Product boundary

Pagestream exists to expose only the Datastar capabilities LeapView uses:
same-origin update streams, signal request decoding, signal patching,
redirects, and the minimal in-process fan-out needed to feed those streams.
Application routes, commands, authorization, signal schemas, and UI behavior
must remain outside this package.

Fan-out uses bounded subscriber mailboxes. Slow subscribers are disconnected
instead of silently losing individual patches or retaining unbounded memory.

Dashboard generation ordering and coalescing live in
`internal/dashboard/stream`; browser client-cookie policy lives in
`internal/platform/web/transport`. Tracing and signal-history inspection are
deliberately excluded. LeapView's development inspector observes the browser's
current Datastar signal state locally and does not call a Pagestream diagnostics
API.

## Updating from upstream

There is no automatic upstream update path. Compare against the commit above,
import only changes that serve the product boundary, update this record, and
run `go test ./pkg/pagestream` plus LeapView's normal CI contract.
