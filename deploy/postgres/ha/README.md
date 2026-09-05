# Same-host PostgreSQL HA qualification

This is a disposable, source-checkout qualification fixture for the native
PostgreSQL topology. It runs two PostgreSQL 18 members under Patroni 4.1.5,
three etcd 3.5.15 DCS members, and an HAProxy endpoint that routes only to the
Patroni primary.

It exercises, in one bounded run:

- a write through HAProxy and streaming replication to the standby;
- abrupt `SIGKILL` of the primary and a Patroni failover write;
- loss and recovery of one etcd DCS member while quorum remains;
- one-at-a-time PostgreSQL member restarts and final convergence.

Run it on Linux with Docker Engine, the Compose plugin, Bash, `jq`, and GNU
`date`/`timeout`:

```sh
task test:qualification:postgres-ha
```

The task is bounded to ten minutes. The runner writes credential-free JSON to
`.tmp/postgres-ha-qualification/evidence.json` and, on success, failure, or a
catchable interrupt, runs:

```sh
docker compose --project-name <isolated-project> --file deploy/postgres/ha/compose.yaml \
  down --volumes --remove-orphans
```

Each run gets a worktree-derived, process-scoped Compose project and unique
markers, so a failed run cannot reuse another run's volumes or observations.
The generated Patroni image is scoped to that same project and removed during
normal cleanup. Set
`LEAPVIEW_POSTGRES_HA_EVIDENCE_DIR` to retain evidence elsewhere, or set
`LEAPVIEW_POSTGRES_HA_WAIT_SECONDS` (10–600) for a bounded local timeout per
readiness/failover wait. The default credentials are disposable and are never
included in evidence; URL-safe overrides are accepted through the three
`LEAPVIEW_POSTGRES_HA_*_PASSWORD` variables.

This validates Patroni/DCS behavior and the leader endpoint on one host. It
does not prove independent failure domains, network partitions, provider
managed PostgreSQL behavior, backup/PITR, or application process HA. Its
`evidenceKind` and output directory are intentionally separate from the
application `--multi-node-process` qualification and from the development
PostgreSQL `.tmp` drill.

As with any shell cleanup trap, an uncatchable process or host `SIGKILL` can
prevent cleanup. The evidence records the unique Compose project name for
exact manual removal; the ten-minute task timeout first sends `TERM` and allows
30 seconds for the normal cleanup path before escalating.
