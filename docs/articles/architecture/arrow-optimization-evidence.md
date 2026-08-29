# Evidence-gated Arrow optimization

This scorecard defines the evidence required before changing LeapView's
dashboard Arrow architecture. It covers the FAI-538 workload and scorecard,
the reproducible microbenchmark foundation from FAI-539, and the current-path
dashboard baseline from FAI-540. It does not authorize a production Arrow
migration, a new cache representation, or a cache lifecycle change.

## Decision boundary

The existing dashboard table path converts governed Arrow batches into an
owned Arrow result, Go rows, dashboard frames, string values, and finally an
all-string Arrow IPC stream. The existing semantic API provides a native Arrow
IPC reference path. A future proposal may compare those paths, but it must not
assume that fewer conversions are automatically better.

The retained dashboard query cache and the direct Arrow stream have different
execution behavior. A cache hit avoids database execution, while the current
native stream deliberately bypasses retained results. Measurements must keep
these cases separate. This evidence program must not change cache identity,
ownership, lifecycle, admission, observability, or prewarming.

## Workload matrix

Every result is generated deterministically. The benchmark fixtures use fixed
arithmetic sequences and a null every 13 values; they do not use wall-clock
time, external data, or random input. The FAI-539 stage fixtures require no
database. The FAI-540 baseline uses a test-only deterministic Arrow database
through the real materialize, dashboard runtime, and HTTP response paths; it
requires no external database or generated artifacts. If a future workload
needs randomness, it must publish and reuse one fixed seed.

| Dimension | Required cases | Purpose |
| --- | --- | --- |
| Shape | Narrow: 8 columns; wide: 32 columns | Expose per-column builder, map, and schema costs |
| Rows | 1, 50, 1,000, and 10,000 | Cover empty-adjacent, normal page, large page, and bounded export-like results |
| Physical values | `int64`, `float64`, boolean, UTF-8 string, binary, timestamp, decimal128, date32, and dictionary string | Exercise fixed- and variable-width buffers, the dictionary-copy fallback, and type fidelity |
| Nulls | Deterministic nulls in every physical type | Detect null-to-empty or null-to-zero regressions |
| Execution state | Cold execution, warm cache hit, and direct native execution | Prevent a serializer win from hiding an extra database query |
| Response | Existing JSON/table response, existing dashboard string IPC, and native IPC reference | Separate response-format cost from query cost |
| Concurrency | 1, 10, 20, and 100 clients in later end-to-end qualification | Detect connection pinning, queueing, and slow-consumer effects |

The FAI-539 package benchmarks intentionally isolate CPU and allocation stages;
they do not simulate database latency, cache lookup, network backpressure, or
concurrent users. FAI-540 adds repeatable direct, cold, and warm request
baselines. FAI-542 must add concurrency and warm-cache qualification before a
production-path decision.

## Representative scenarios

The evidence set must include these dashboard/query scenarios:

1. An eligible detail table without visual calculations, matrix, or pivot
   shaping. This is the only initial direct-Arrow candidate.
2. A wide detail table containing native numeric, temporal, binary, decimal,
   string, and nullable values.
3. A warm detail-table cache hit using the existing retained-result path.
4. A cold detail-table request using the existing retained-result path.
5. The existing native semantic Arrow stream as a serialization reference,
   not as proof that dashboard execution should bypass its cache.
6. KPI, bundle, calculated-table, matrix, and pivot requests as controls. They
   remain on their existing paths until separately measured and approved.
7. Cancellation before the first batch, cancellation during streaming, an
   empty result with a valid schema, pagination at the page boundary, and a
   slow response consumer in later end-to-end qualification.

For cache comparisons, report at least these three lanes independently:

| Lane | Database work | Retained-result lookup | Serialization path |
| --- | --- | --- | --- |
| Existing cold dashboard | Yes | Miss and populate | Decode, shape, stringify, encode |
| Existing warm dashboard | No | Hit with bounded lease | Decode, shape, stringify, encode |
| Native direct reference | Yes | Bypassed | Native record batches to IPC |

Do not combine those lanes into one average. A direct encoder cannot be adopted
if it improves encoding while making a normally warm request execute DuckDB.

## Stage benchmarks

The benchmark harness is deliberately small and does not import ignored
generated dashboard API packages:

- `BenchmarkArrowCaptureCopy` calls the real immutable Arrow result builder and
  measures both the scalar deep-copy boundary and dictionary IPC fallback.
- `BenchmarkArrowDecodeRows` calls the real Arrow-to-owned-Go-row decoder,
  including dictionary lookup.
- `BenchmarkDashboardRowShaping` measures the current named-map to ordered-row
  projection algorithm with the same deterministic value shapes.
- `BenchmarkDashboardStringProjection` isolates the current cell-to-string
  projection used before dashboard IPC encoding.
- `BenchmarkArrowIPCExistingDashboardString` models the current all-string
  Arrow schema and encoder from already projected strings.
- `BenchmarkArrowIPCNativeReference` writes the same native record directly to
  Arrow IPC as the reference serialization boundary.
- `BenchmarkDashboardBaselineEndToEnd` drives the current dashboard query,
  frame, JSON, and all-string Arrow IPC paths through the real runtime and HTTP
  response functions. It reports response bytes, physical-query and cache
  outcomes, retained/transient Arrow ownership, and the existing timing fields.
- `BenchmarkDashboardBaselineStages` separates a warm query and frame build,
  JSON serialization, string projection, and Arrow IPC generation and
  buffering without replacing any production implementation.

The dashboard shaping and string-projection benchmarks are test-only mirrors,
not alternate production implementations. The FAI-540 fixtures are also
test-only, but they call the actual materialize, dashboard, and HTTP functions.

Run the bounded development set with one logical CPU and three samples:

```sh
task bench:arrow:quick
```

Run every width and row-count combination with ten samples for comparison:

```sh
task bench:arrow:full
```

The quick command is a smoke and iteration check. Only the full command is
suitable for a benchmark comparison. Neither command is part of pull-request
CI because timing thresholds on shared runners would be misleading.

## FAI-540 current dashboard baseline

The baseline keeps the HTTP API lane and dashboard runtime cache lanes separate.
The API handler deliberately labels its request as `SurfaceAPI`, so it performs
physical work for both JSON and Arrow requests and does not exercise the
retained dashboard-result lookup. The dashboard runtime lanes retain the
ordinary `SurfaceDashboard` behavior. `api_direct` is the current tabular
dashboard API path, not the native semantic Arrow reference from FAI-539.

| Baseline lane | What the harness measures | Expected cache evidence |
| --- | --- | --- |
| API direct, JSON or Arrow | Real tabular HTTP handler from query through response bytes | Physical queries; no hit, miss, or retained result |
| Dashboard cold, JSON or Arrow | Real dashboard runtime query and frame plus the real response serializer | Miss, populate, bounded lease, and physical queries |
| Dashboard warm, JSON or Arrow | Same runtime and serializer after one untimed population | Hit and zero physical queries |

The cold lane invokes the materialize runtime's existing public cache reset
outside the timed region, then observes the normal request miss through the
existing context observer. It neither reaches into cache internals nor changes
cache identity, lifecycle, ownership, or serving behavior.

Each lane covers narrow and wide detail tables, matrix shaping, and pivot
shaping at 1, 50, and 1,000 rows. The test database supplies native `int64`,
`float64`, boolean, UTF-8, binary, timestamp, decimal128, date32, and dictionary
values with deterministic nulls. The 1,000-row ceiling matches the current
dashboard request limit; the FAI-539 stage benchmarks retain the 10,000-row
export-like case.

The following representative results are medians of ten 500 ms samples from
FAI-540 benchmark commit `a59f6804`, using Go 1.25.14, Linux amd64, an AMD
EPYC-Rome virtual CPU, and `-cpu=1`. They use the wide detail workload with 32
input columns and 1,000 rows. Allocated MiB is derived from Go's `B/op`; it is
not process RSS.

| Lane | Response | Latency | Allocated MiB/op | Allocs/op | Response bytes | Physical queries/op |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| API direct | JSON | 53.18 ms | 20.33 | 175,600 | 324,281 | 2 |
| API direct | Arrow | 38.84 ms | 18.51 | 123,619 | 368,160 | 2 |
| Dashboard cold | JSON | 52.65 ms | 20.40 | 175,583 | 324,281 | 2 |
| Dashboard cold | Arrow | 38.17 ms | 18.54 | 123,612 | 368,160 | 2 |
| Dashboard warm | JSON | 47.00 ms | 17.57 | 163,469 | 324,281 | 0 |
| Dashboard warm | Arrow | 33.66 ms | 16.12 | 111,502 | 368,160 | 0 |

The matching stage medians make the current-path costs more legible:

| Current stage | Latency | Allocated MiB/op | Allocs/op |
| --- | ---: | ---: | ---: |
| Warm query and dashboard-frame construction | 31.03 ms | 12.91 | 101,806 |
| JSON serialization | 17.71 ms | 4.06 | 61,626 |
| Dashboard string projection | 1.54 ms | 0.57 | 8,382 |
| Arrow IPC generation and response buffering | 2.46 ms | 2.27 | 1,254 |

These numbers establish a comparison point; they do not establish an adoption
result. On this fixture, the current Arrow response is about 13.5% larger than
JSON. It is faster to serialize, but its schema contains only UTF-8 fields:
source nulls become empty strings and native numeric, temporal, binary,
decimal, and dictionary physical types are represented as strings. The
fidelity tests lock those current boundaries so a future proposal must explain
any contract change rather than treating it as a performance-only change.

The benchmark reports the existing planning, connection-wait,
database-and-capture, and execution timing fields. `database-and-capture-ms/op`
includes deterministic record production, capture, and ownership work and is
quantized to integer milliseconds; it is not an isolated DuckDB timer.

The baseline has deliberate limitations:

- the deterministic database excludes DuckDB I/O, connection contention, and
  production data distributions;
- `httptest.ResponseRecorder` excludes network transport, browser decode, and
  slow-consumer backpressure;
- cached runtime lanes call the real query, frame, and response serializer
  functions directly but exclude Datastar SSE framing and fan-out;
- the single-CPU harness does not measure concurrent clients, cancellation,
  process RSS, or production p50/p95/p99 latency; those remain qualification
  work, principally FAI-542;
- no production instrumentation, dashboard behavior, cache implementation, or
  materialization ownership behavior changes are part of FAI-540.

For a focused profile of the real current stages, use one workload:

```sh
mkdir -p .tmp/arrow-bench
go test ./internal/dashboard/http -run '^TestDashboardBaseline' \
  -bench '^BenchmarkDashboardBaselineStages$/detail_wide/rows_1000$' \
  -benchmem -benchtime=2s -count=1 -cpu=1 \
  -cpuprofile .tmp/arrow-bench/dashboard-wide.cpu.pprof \
  -memprofile .tmp/arrow-bench/dashboard-wide.mem.pprof
```

## Required measurements

Capture raw results rather than reporting a single percentage:

| Measure | Source | Required report |
| --- | --- | --- |
| Latency | Go `ns/op`; later end-to-end request timings | Samples plus p50, p95, and p99 for request lanes |
| Throughput | Go `MB/s`; later completed requests per second | Per workload and concurrency level |
| Allocations | Go `B/op` and `allocs/op` | Absolute values and relative change |
| Memory | Go memory profile, Arrow retained/transient bytes, and process RSS | Peak and steady state; include allocator scope |
| CPU | Go CPU profile and process CPU time | Hot paths and CPU/request |
| IPC size | `ipc-bytes/op` | Absolute bytes and relative change |
| Type fidelity | Decoded schema/value assertions | Exact physical type, decimal, timestamp, binary, and null results |
| Operational correctness | Focused tests and end-to-end qualification | Pagination, authorization, policy, audit, cancellation, ownership, and empty results |

For a focused CPU and memory profile, run one workload rather than mixing
profiles from the complete matrix. For example:

```sh
mkdir -p .tmp/arrow-bench
go test ./pkg/arrowresult -run '^$' \
  -bench '^BenchmarkArrowIPCNativeReference$/wide/rows_10000$' \
  -benchmem -benchtime=2s -count=1 -cpu=1 \
  -cpuprofile .tmp/arrow-bench/native-wide.cpu.pprof \
  -memprofile .tmp/arrow-bench/native-wide.mem.pprof
```

For machine-readable retention, add `-json` to the underlying `go test`
command and save stdout with the commit, Go version, operating system,
architecture, CPU model, `GOMAXPROCS`, and whether the machine was otherwise
idle. Keep the ordinary output too: it is accepted by `benchstat` for
before/after confidence intervals. Compare at least ten full samples from the
same host and toolchain; do not compare results collected on different CPU
models as if they were paired samples.

The unit test `TestArrowBenchmarkCalibrationDetectsExtraCopy` injects one
additional payload copy and confirms that the allocation measurement detects
the controlled regression. It qualifies the harness signal, not a production
threshold.

## Adoption scorecard

A proposal may proceed from measurement to a bounded production prototype only
when all correctness gates pass and at least one benefit gate is met.

### Correctness gates

- Native physical types match the documented Arrow contract for every supported
  fixture type.
- Null remains distinct from empty string, zero, and false.
- Decimal and timestamp values remain exact, including scale and UTC behavior.
- Empty results emit a usable schema.
- Pagination, query/snapshot metadata, cursor trailers, cancellation, result
  budgets, authorization, row policies, masks, and auditing remain correct.
- Borrowed DuckDB records never escape their synchronous callback, and retained
  data remains valid until its final lease is released.
- Race, leak, and slow-consumer qualification shows no unbounded growth or
  cross-principal/cross-policy data reuse.

Any correctness failure rejects adoption regardless of speed.

### Benefit gates

At least one of these must hold for a representative eligible detail workload:

- allocations per operation or allocated bytes per operation improve by at
  least 20%;
- CPU cost or end-to-end p95 latency improves by at least 10%; or
- native type/null correctness provides a required capability unavailable from
  the existing response, with every regression guardrail still satisfied.

### Regression guardrails

- No representative workload may regress p95 or CPU/request by more than 5%
  without a documented correctness tradeoff and maintainer approval.
- Throughput must not fall by more than 5% at any required concurrency level.
- Peak RSS, retained Arrow bytes, and IPC size must not grow by more than 10%
  unless the increase is explained and bounded.
- A direct path must not replace a warm cache hit with database execution.
- No new production abstraction remains if its hypothesis is rejected.

A result inside the noise interval is "no demonstrated improvement," not a
win. Record adopted, revised, rejected, or deferred for each hypothesis, along
with raw evidence, confidence output, the tested commit, and rollback path.

## Remaining gates

FAI-538 and FAI-539 establish the scorecard and microbenchmark foundation.
FAI-540 establishes the reproducible current dashboard round-trip baseline;
it does not qualify production data or concurrency. FAI-541 must lock the
response contract, and FAI-542 must qualify warm cached projection and
concurrency. Direct detail-table or lease-bounded projection prototypes remain
prohibited until those issues provide evidence that clears this scorecard.
