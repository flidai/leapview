# Evidence-gated Arrow optimization

This scorecard defines the evidence required before changing LeapView's
dashboard Arrow architecture. It covers the FAI-538 workload and scorecard,
the reproducible microbenchmark foundation from FAI-539, the current-path
dashboard baseline from FAI-540, and the warm-cache qualification from
FAI-542. FAI-543 adds a test-only direct governed streaming prototype after
FAI-541 locked the `native-v1` response contract. It does not authorize a
production Arrow migration, a new cache representation, or a cache lifecycle
change.

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
| Concurrency | 1, 10, 20, and 100 simultaneous clients | Detect CPU saturation, allocation pressure, queueing, and slow-consumer risks |

The FAI-539 package benchmarks intentionally isolate CPU and allocation stages;
they do not simulate database latency, cache lookup, network backpressure, or
concurrent users. FAI-540 adds repeatable direct, cold, and warm request
baselines. FAI-542 adds concurrency and warm-cache qualification while still
stopping short of a production-path decision.

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
- `BenchmarkWarmArrowCacheLookupLease` isolates public retained-result lookup
  plus independent lease acquisition and release from decoding.
- `BenchmarkDashboardWarmShapingStages` attributes allocations to datum maps,
  normalization, ordered frames/windows, calculation cloning, and envelope
  construction using the production shaping functions.
- `BenchmarkDashboardWarmCacheConcurrency` executes exact simultaneous-user
  batches through the governed warm path and requires cache hits with zero
  physical queries for every measured request.
- `BenchmarkDashboardWarmSerializationStages` compares production JSON and
  all-string table IPC with a test-only native IPC reference over the same
  deterministic physical values.
- `BenchmarkDashboardDirectArrowExperiment` compares the current `api_direct`
  transformation boundary with the build-tagged adapter over the governed
  direct Arrow executor and existing native-v1 sink. Both timed lanes require
  one physical query and no retained-cache outcome. It runs only through the
  dedicated `bench:arrow:direct-streaming-experiment` command.

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

## FAI-542 warm-cache qualification

FAI-542 tests whether retained Arrow decoding, dashboard shaping, and response
serialization are material warm-path costs. It does not implement a native
dashboard response. Each real-path workload first executes an untimed cold
request through the governed runtime and requires a miss plus physical work.
The identical warm probe and every measured request must then report only hits
and zero physical queries. Returning from the cold request is the retention
completion boundary; the fixture does not inspect cache keys, generations, or
lifecycle internals.

The required real workloads are:

- a one-row KPI serialized through the visualization JSON response;
- four compatible charts executed by the real dashboard bundle optimizer and
  materialized bundle path;
- a 32-column, 1,000-row multi-measure chart serialized as visualization JSON;
- a 32-column, 1,000-row table window serialized as JSON and through the
  current all-string Arrow response.

One benchmark operation is one simultaneous-user batch. Every user shares the
same arrival timestamp, so reported request percentiles include scheduler and
queueing delay from the batch boundary. Goroutine setup is outside the timer.
The canonical run uses one logical CPU to keep runs comparable and expose
CPU/allocation saturation; it is not a production capacity forecast.

### Evidence levels and provenance

The earlier three-sample, 100 ms run was a **preliminary observation** used to
calibrate the fixtures. It is not comparison input and does not clear any
decision gate. `task bench:arrow:quick` intentionally retains that smaller
policy as a bounded smoke command.

The results below are the **comparison-grade baseline** collected by the full
protocol on 2026-08-30:

- tested commit: `cba1bc1206062b0878527b2b8691a38287795c03`;
- Linux 7.0.0-29-generic, amd64, Go 1.25.14;
- 16 visible single-thread AMD EPYC-Rome virtual cores, with every benchmark
  forced to `-cpu=1`;
- deterministic arithmetic inputs and a null at every position satisfying
  `(row + column) % 13 == 0`;
- ten samples for every benchmark; 500 ms minimum benchmark time for capture,
  decode, cache lookup, shaping, baseline, and serialization, and 250 ms for
  each exact warm-concurrency batch;
- the final capture started without another observed Go test on the shared
  host. Earlier discarded attempts encountered host contention and temporary
  storage pressure, so this is not presented as dedicated-hardware capacity
  data.

The exact command was:

```sh
task bench:arrow:full
```

The Taskfile expands that command into the eight package-local commands shown
in the repository artifact
`docs/articles/architecture/benchmark-data/fai-542-cba1bc12-full.txt`.
That 2,316-line file is the unedited stdout/stderr capture; its SHA-256 is
`4c35d6a980262ce1c259d0d0f2f6fcb9b4e7abe35399ef203e664977df905ee7`.
All eight commands ended in `PASS`. Tables below report the median of ten
samples and, where useful, the observed minimum and maximum. Allocated MiB is
derived from Go `B/op`, not process RSS. There is no response-equivalent
candidate implementation to pair with this baseline, so these numbers do not
constitute a `benchstat` before/after confidence claim.

### Warm concurrency results

Every measured sample remained warm. KPI and wide-chart requests observed one
hit each, bundle requests observed four hits, and table windows observed two
hits. Misses, coalesced outcomes, physical-query observations, and database
calls were all zero.

| Workload | p95 @ 1, median [range] | p95 @ 100, median [range] | Requests/s @ 1 | Requests/s @ 100 | Bytes/request |
| --- | ---: | ---: | ---: | ---: | ---: |
| KPI | 1.33 [1.28, 1.42] ms | 85.7 [81.1, 88.4] ms | 1,094 | 1,126 | 1,100 |
| Four-chart bundle | 9.07 [7.67, 12.0] ms | 591 [563, 645] ms | 155 | 163 | 10,474 |
| Wide chart | 78.4 [74.6, 85.6] ms | 7,538 [7,486, 7,681] ms | 13.48 | 13.22 | 317,001 |
| Table window JSON | 54.7 [51.2, 80.2] ms | 5,164 [5,121, 5,368] ms | 19.31 | 19.28 | 324,281 |
| Table window Arrow | 40.6 [38.8, 44.2] ms | 3,760 [3,710, 3,945] ms | 26.84 | 26.45 | 368,176 |

Wide-chart and table throughput remains approximately flat while p95 grows
almost linearly with simultaneous users on one CPU. Allocations per request
also remain high and stable: about 24.2 MiB/444,824 allocations for the wide
chart, 18.1 MiB/167,232 allocations for table JSON, and 16.5 MiB/115,260
allocations for table Arrow at one user. This fixture is CPU/allocation bound,
not database bound. KPI and bundle payloads are much lighter and do not by
themselves justify an Arrow response change.

### Stage attribution

Stage benchmarks call production conversion functions but deliberately
overlap: datum-map creation includes value normalization, and envelope
construction includes calculation cloning and validation. Do not sum every
row as if the stages were disjoint. They identify where CPU and allocations
are created.

| Warm stage | Representative shape | Latency | Allocated MiB/op | Allocs/op |
| --- | --- | ---: | ---: | ---: |
| Cache lookup and independent lease | 32 × 1,000 | 0.00027 ms | 0.00013 | 3 |
| `DecodeRows` | 32 × 1,000 | 8.77 ms | 3.54 | 70,520 |
| Chart datum maps | 32 × 1,000 | 8.18 ms | 4.27 | 9,001 |
| Value normalization | 32 × 1,000 | 2.78 ms | 0.98 | 42,671 |
| Ordered chart frame | 32 × 1,000 | 1.49 ms | 0.51 | 1,002 |
| Calculation-free frame clone | 32 × 1,000 | 0.61 ms | 0.51 | 1,002 |
| Visualization envelope | 32 × 1,000 | 2.95 ms | 0.58 | 1,075 |
| Table datum maps | 32 × 1,000 | 12.22 ms | 4.92 | 51,667 |
| Ordered table window | 32 × 1,000 | 1.81 ms | 0.59 | 1,077 |
| Wide-chart JSON response | 32 × 1,000 | 15.83 ms | 3.98 | 67,708 |
| Table JSON response | 32 × 1,000 | 17.66 ms | 4.06 | 61,626 |

Cache lookup is effectively constant across 50/1,000 rows and 8/32 columns,
at roughly 0.26 microseconds and 136 bytes per lookup. Decode, copied datum
maps, normalization, and serialization dominate the large warm workloads.

### Current string IPC versus native reference

The native reference is test-only. It encodes an independently constructed
lease containing the same deterministic physical values; it does not read the
production result-cache entry and is not a response-compatible dashboard
implementation.

| Serialization stage | Latency | Allocated MiB/op | Allocs/op | IPC bytes |
| --- | ---: | ---: | ---: | ---: |
| Current string projection | 1.47 ms | 0.57 | 8,382 | — |
| Current all-string IPC | 2.20 ms | 2.27 | 1,254 | 368,176 |
| Native IPC reference | 0.45 ms | 1.04 | 304 | 267,008 |

Against the sum of string projection and current IPC medians, the optimistic
native reference is about 88% lower in isolated time, 63% lower in allocated
bytes, and 97% lower in allocations. Its payload is 27.5% smaller. The native
reference range was 0.397–0.638 ms, still separated from either current stage.
It also preserves native `int64`, `float64`, boolean, UTF-8, binary, timestamp,
decimal128, date32, and dictionary types, schema/field metadata, exact values,
and null bitmaps. The current response retains its documented all-UTF8 and
null-to-empty behavior.

This is an optimistic upper bound, not a response-equivalent candidate. Even
subtracting the isolated median difference from the 40.6 ms warm table-Arrow
p95 would imply only about 7.9%, below the 10% end-to-end performance gate;
microbenchmark stages are not asserted to add exactly to request latency. The
larger unanswered question is before serialization: decode and datum-map
creation are independently material, but only a prototype can measure how
much of that work is safely avoidable.

### FAI-542 decision: narrow prototype scope

The comparison-grade baseline supports proceeding only to an isolated,
lease-bounded Arrow-to-ordered-frame prototype for the wide detail/table
workload. It does **not** prove the thesis, clear a production-adoption gate, or
support a broad native response change:

- a serializer-only experiment is unlikely to clear the end-to-end latency
  gate;
- the expensive decode/map boundary is material enough to justify a narrowly
  scoped experiment;
- native IPC provides a strong stage-level allocation and fidelity signal, but
  remains an optimistic upper bound;
- the native reference is not dashboard-response equivalent and therefore
  cannot satisfy the correctness gates by itself;
- KPI and bundle results do not establish a broad dashboard benefit;
- calculated tables, matrix, and pivot remain separate candidates.

Any later prototype must be benchmark-only, rerun the same ten-sample protocol
against this baseline on the same idle host, and pass exact value/null/schema
fidelity before its result can be classified as proceed, revise, or reject.
Adoption remains prohibited until response compatibility, lease lifetime,
cancellation, slow-consumer memory, and multi-CPU qualification pass.

An earlier preliminary run generated CPU and memory profiles for the wide-chart
lane, but the local stripped Go toolchain does not include the `pprof` reader.
Those profiles are not comparison-grade decision input. The full run provides
`B/op`, allocations, single-CPU latency, throughput, and payload size; process
RSS and profile-stack attribution remain unavailable rather than inferred.

## FAI-543 direct governed Arrow streaming prototype

FAI-543 is a build-tagged experiment for ordinary detail tables. It adds no
HTTP route, feature flag, production handler, serving-path branch, cache
behavior, retained-result behavior, installer behavior, or migration. The
adapter is compiled only with `fai543experiment` and calls the existing
governed `ExecuteDataQueryArrow` API plus the existing semantic native-v1 IPC
sink. This prototype does not prove production readiness.

### Experiment design

The control is the current `api_direct` path:

```text
governed query -> DuckDB Arrow -> owned Arrow copy -> DecodeRows -> maps
-> normalization -> dashboard frame -> string projection -> all-string IPC
```

The candidate is deliberately smaller:

```text
governed query -> borrowed DuckDB Arrow batches -> existing native-v1 IPC sink
```

Both lanes use `SurfaceAPI`, one policy governor call, one physical query, and
zero retained-cache outcomes. The benchmark does not compare either lane with
a warm dashboard cache hit. Warm-cache evidence from FAI-542 remains a
guardrail: a future serving proposal must not replace a hit with DuckDB work.
An executable comparison contract also requires identical fields, filters,
sort, offset, physical limit, `dashboard_rows` operation, principal/policy
identity, admission request, and result-budget accounting in both lanes.

The candidate accepts only compiler-resolved detail queries rendered as an
ordinary table, block `a`, without visual calculations. Matrix, pivot,
multi-block shaping, and Datastar SSE are rejected by the experiment harness.
The test workloads are 8- and 32-column tables with 50- and 999-row final
pages. The 999-row case reaches the current 1,000-row request ceiling without
triggering the control's separate exact-count query, so both measured lanes
perform the same one physical query. Full-page pagination is qualified as a
correctness case, not included in the timing comparison because the current
control performs a second query there.

Borrowed records are rebound to a response-safe schema and written
synchronously by the existing IPC sink. The temporary record and any
pagination slice are released before the sink callback returns. Because the
Arrow IPC writer memoizes dictionary values until close, the adapter copies
only dictionary values into experiment-owned memory; dictionary indices and
all ordinary arrays remain borrowed and synchronous. A producer
release-before-close test proves the source allocator reaches zero while the
IPC stream remains open. No producer-owned Arrow batch, column, or buffer is
stored by the prototype adapter.

### Correctness and governance result

The candidate passed the FAI-541 native-v1 oracle for field aliases, ordering,
nullability, empty-result schema, public metadata, all signed and unsigned
integer widths, float32/64, decimal128, date32/date64, timestamp with and
without timezone, UTF-8, binary, dictionary values, and exact null positions.
An interleaved dimension/metric/dimension/metric fixture proves that the
declared visualization projection is restored after the governed query planner
groups dimensions and metrics. The comparison also requires byte-identical SQL
and physical projection columns for control and candidate.
It also passed filter, stable sort, offset, limit, limit-plus-one pagination,
scope-bound cursor, cancellation before and during streaming, partial-write,
post-commit failure, and slow-consumer lifetime tests.

Governance remains owned by the existing executor. The paired harness requires
one governor call in both lanes, and an admission rejection must happen before
planning, database execution, or response commitment. The FAI-541
`queryauthz` oracle remains the shared proof for authorization, row-policy,
column-mask, governed alias, credential/principal audit identity, correlation
identity, and denial before physical execution. Result schema and record
budgets remain charged by the existing Arrow producer; the adapter additionally
charges the positive size delta of native-v1 response metadata before response
commitment. A near-limit regression rejects that response without emitting an
Arrow body or success cursor. The prototype neither copies those validators
nor adds an alternate authorization or query path.

### Benchmark protocol and results

The comparison-grade command is:

```sh
task bench:arrow:direct-streaming-experiment
```

It runs ten 500 ms samples on one logical CPU and reports `ns/op`, `B/op`,
`allocs/op`, IPC bytes, physical queries, cache outcomes, and per-operation
p50/p95/p99 observations. Results below were collected on 2026-08-31 with Go
1.25.14, Linux amd64, and an AMD EPYC-Rome virtual CPU. The benchmarked
implementation is the reconciliation of remote PR head `89d4406e` and reviewed
architecture commit `2c35464c`; the exact non-documentation implementation
patch has SHA-256
`bfdc2aab6095e3775b1d4ab2fa04e3e282a77f892b64f91863025c63329d5cd3`.
The final review commit did not yet exist when the run was captured. Values are
medians of the ten samples; allocated memory is Go `B/op`, not process RSS.

| Workload | Lane | Latency | B/op | Allocs/op | IPC bytes |
| --- | --- | ---: | ---: | ---: | ---: |
| 8 × 50 | Control `api_direct` | 0.743 ms | 260,901 | 2,816 | 6,176 |
| 8 × 50 | Candidate native-v1 | 0.235 ms | 106,170 | 686 | 5,960 |
| 8 × 999 | Control `api_direct` | 6.440 ms | 2,735,669 | 35,775 | 91,296 |
| 8 × 999 | Candidate native-v1 | 1.055 ms | 714,802 | 2,324 | 71,968 |
| 32 × 50 | Control `api_direct` | 3.368 ms | 1,564,990 | 9,704 | 23,840 |
| 32 × 50 | Candidate native-v1 | 1.020 ms | 537,604 | 2,564 | 23,448 |
| 32 × 999 | Control `api_direct` | 40.795 ms | 19,176,417 | 123,461 | 367,680 |
| 32 × 999 | Candidate native-v1 | 4.630 ms | 3,063,061 | 11,698 | 268,816 |

For the representative wide workloads, the median of each sample's observed
request percentiles was:

| Workload | Lane | p50 | p95 | p99 |
| --- | --- | ---: | ---: | ---: |
| 32 × 50 | Control `api_direct` | 2.906 ms | 5.511 ms | 5.946 ms |
| 32 × 50 | Candidate native-v1 | 0.736 ms | 1.872 ms | 2.198 ms |
| 32 × 999 | Control `api_direct` | 40.274 ms | 44.866 ms | 44.866 ms |
| 32 × 999 | Candidate native-v1 | 4.772 ms | 6.126 ms | 6.405 ms |

The equal control p95/p99 at 32 × 999 is a resolution limit: each 500 ms
sample completed only 13–15 operations. It must not be interpreted as a
precise tail distribution. The paired median signal at 32 × 999 is 88.7%
lower latency, 84.0% fewer allocated bytes, 90.5% fewer allocations, and a
26.9% smaller IPC payload. The 32 × 50 case is 69.7%, 65.6%, 73.6%, and 1.6%
lower respectively. These are deterministic single-process fixture results,
not production capacity estimates or confidence intervals.

Focused CPU and memory profiles can be captured independently so the two lanes
are not mixed:

```sh
mkdir -p .tmp/arrow-bench
go test -tags=fai543experiment ./internal/dashboard/http -run '^$' \
  -bench '^BenchmarkDashboardDirectArrowExperiment$/detail_wide/rows_999/control_api_direct$' \
  -benchmem -benchtime=2s -count=1 -cpu=1 \
  -cpuprofile .tmp/arrow-bench/fai-543-control.cpu.pprof \
  -memprofile .tmp/arrow-bench/fai-543-control.mem.pprof
go test -tags=fai543experiment ./internal/dashboard/http -run '^$' \
  -bench '^BenchmarkDashboardDirectArrowExperiment$/detail_wide/rows_999/candidate_governed_native_v1$' \
  -benchmem -benchtime=2s -count=1 -cpu=1 \
  -cpuprofile .tmp/arrow-bench/fai-543-candidate.cpu.pprof \
  -memprofile .tmp/arrow-bench/fai-543-candidate.mem.pprof
```

The focused profile commands remain the reproducible next step for stack
attribution. No profile-derived claim from the superseded custom-encoder
implementation is retained here.

### Resource behavior and limitations

The governed executor remains synchronous. A blocking response-writer test
proves execution stays active for the entire slow-consumer write and releases
only after the sink returns. That preserves the documented database lease and
runtime-generation lifetime, but it also means a real slow client pins those
resources. The experiment does not measure production network backpressure,
DuckDB I/O, connection-pool contention, browser decoding, multi-CPU throughput,
or process RSS. `httptest.ResponseRecorder` buffers the response, so `B/op`
includes the fixture's response buffer and is not a bound on production peak
RSS. No retained Arrow result or cache lease was created in either lane.

The candidate harness performs compiler resolution, governed query
construction, admission, physical execution, metadata validation, and HTTP
response buffering, but it is not a production handler and does not reproduce
every current route/page lookup or JSON request-decoding instruction. Those
small fixed costs are therefore not isolated from the measured benefit. FAI-544
must compare response-equivalent production composition before treating the
latency delta as an end-to-end estimate.

The prototype therefore supports proceeding to FAI-544 qualification: it
passes the response/governance oracle and clears the scorecard's allocation,
latency, and payload signals on the eligible deterministic workloads. FAI-544
must still qualify real DuckDB batches, production authorization composition,
connection/backpressure limits, cancellation under real transport, process
memory, browser compatibility, multi-CPU behavior, and warm-cache routing
before any production proposal is considered.

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
it does not qualify production data. FAI-542 establishes a comparison-grade
warm baseline. FAI-541 locks the `native-v1` response contract. FAI-543 shows a
strong synthetic signal for direct governed streaming but also exposes an
unresolved exact-count versus pagination-probe difference. None of these
issues authorizes production behavior. Direct production detail-table
delivery remains prohibited until a
response-equivalent candidate demonstrates a measured benefit and response
compatibility, lease lifetime, cancellation, slow-consumer memory, multi-CPU
behavior, and the remaining correctness gates clear this scorecard.
