# Compatible managed application maintenance

`leapviewctl host managed-release` implements a bounded, host-local image
handoff for the dedicated Kamal application host. It uses Kamal 2.12.0 and the
enrolled immutable kamal-proxy v0.9.2 image. It does not migrate a database or
restore customer files. A compatible predecessor restarts against the current
state, including writes acknowledged by the candidate.

This implementation is a qualification candidate. The full managed-host journey,
public TLS/SSE/upload behavior, provider failures and production adoption still
require protected profile evidence. Unit, real SQL, private-socket and pinned
Kamal command tests do not substitute for that evidence.

## Enrollment and authority

Use a dedicated Linux application host with the NixOS Docker/network baseline,
one `web` role, loopback root SSH with a pinned host key, and no other running
containers. The application and controller inspect the same local Docker socket.
The shared home has one owner and is an identity bind mount. Public ingress is
exclusively the dedicated Kamal proxy. Existing deployments without the private
maintenance socket must undergo separately qualified enrollment first.

The private operator directory contains these versioned files:

- `deploy.yml`, copied from `kamal/maintenance_deploy.yml.example`;
- `ssh_config`, copied from `kamal/maintenance_ssh_config.example`;
- `Gemfile`, `Gemfile.lock`, `probe_host.rb`, `maintenance_adapter.rb` and
  `maintenance_config.rb` from this revision, with the locked bundle installed;
- `.kamal/secrets` and `environment.json` containing the enrolled secret inputs.

`environment.json` is a JSON object of the `LEAPVIEW_*` and `KAMAL_*` environment
values consumed by the template and secret file. It is mode 0600. The controller
does not inherit ambient deployment variables, Docker contexts or Ruby options.
Projection stdout contains resolved credentials and is captured privately in
memory; do not invoke the projection script in a logged shell.

The operator directory, admission store and controller state directory must be
root-owned and private. Inputs must have trusted directory ancestry and cannot
be symlinks or group/world-writable files. Keep the controller state outside
the application home, database volumes and every restore scope. Use the same
controller state root for other host maintenance commands, so both live locks
and unfinished durable operations exclude one another.

The root-owned mode-0600 profile has this shape (paths are examples):

```json
{
  "version": 1,
  "target": "production-app-1",
  "root": "/opt/leapview-managed",
  "stateRoot": "/var/lib/leapview-controller",
  "home": "/var/lib/leapview/home",
  "socket": "/var/lib/leapview/home/maintenance.sock",
  "service": "leapview",
  "hostname": "analytics.example.com",
  "proxyImage": "basecamp/kamal-proxy@sha256:<qualified-64-hex-digest>",
  "admissionRoot": "/opt/leapview-admissions"
}
```

Provision `stateRoot/ingress.json` as a private file containing
`{"publish":false}` during initial enrollment. Its value becomes controller-owned
after enrollment. The predecessor must already support startup-closed maintenance
admission. Both admitted app images and the qualified proxy image must be retained
locally; the enrolled `basecamp/kamal-proxy:v0.9.2` reference must resolve to the
same content as `proxyImage`. Handoff and recovery do not log into a registry or
pull images. The release producer remains responsible for acquiring and
authenticating these artifacts and the proxy version mapping before enrollment.

The authenticated release producer supplies the private request and canonical OCI
admission receipts. Each receipt is stored under
`admissionRoot/<artifact-admission-sha256-without-prefix>.json`; its canonical
digest, repository/image and exact source revision are checked by the controller.
This is the existing artifact-admission authority contract, not a new admission
issuer. Copy receipts only through the authenticated producer handoff. Never
manufacture receipts from booleans or use a fixture as live authorization.

The request contains `version: 1`, `target`, `predecessor`, `candidate`,
`sourceBefore`, `sourceAfter` and `budgets`. Each release binds `image`, the full
40-character `revision`, `artifactAdmissionDigest`, `configurationDigest` and
`credentialDigest`. The producer collects source compatibility from those exact
admitted revisions: permission profile, complete migration hashes, engine module
versions and role-policy hash. The host recomputes image-only compatibility;
schema, engine, role, configuration and credential transitions are rejected.
`budgets.phase` and `budgets.total` are positive nanosecond durations, total at most
24 hours (for example 120000000000 and 900000000000).

To obtain enrollment fingerprints without printing credentials:

```sh
leapviewctl host managed-release inspect --profile /run/leapview/profile.json \
  --image ghcr.io/flidai/leapview@sha256:<digest> --revision <full-commit>
```

The configuration digest covers the profile and every listed operator file.
The credential digest covers the complete resolved container environment,
including image defaults, except `KAMAL_VERSION`, `KAMAL_HOST` and
`KAMAL_CONTAINER_NAME` metadata. Both
releases must resolve to the same digest, which must also match the live process.
The producer must preserve the enrolled read-only trust mounts and external
service configuration throughout this operation. Credential rotation and external
configuration changes require their separately admitted maintenance flow.

## Handoff and recovery

```sh
leapviewctl host managed-release run \
  --profile /run/leapview/profile.json --request /run/leapview/request.json
leapviewctl host managed-release status \
  --profile /run/leapview/profile.json --request /run/leapview/request.json
leapviewctl host managed-release recover \
  --profile /run/leapview/profile.json --request /run/leapview/request.json
```

Keep the exact private profile/request available for recovery. The controller:

1. Acquires the shared host lock and validates the artifact, configuration,
   credentials, retained images and exclusive process/ingress inventory.
2. Records closure intent durably. The predecessor immediately closes HTTP
   admission and cancels SSE subscriptions. Workers remain authorized during
   ordinary request drain, then stop and drain before the closure RPC acknowledges
   success. Work admission is considered closed only after that acknowledgement;
   a timeout cannot advance the handoff to another process owner.
3. Writes the private ingress gate and recreates the proxy without host ports.
   It gracefully stops the app, verifies exit status and acquires/releases the
   home lock to prove the previous owner is gone.
4. Boots the exact candidate digest through the pinned Kamal adapter. Its HTTP
   and worker admission starts closed. The proxy's private preparation probe
   checks resources without running background jobs.
5. Verifies process identity, resolved environment, database role/schema health,
   credential setup coverage and worker configuration. Only explicit `open`
   authorizes worker execution, under a finite provisional lease.
6. Recreates the proxy with public ports, checks local HTTPS `/readyz` with normal
   certificate verification, records commit durably, and finalizes work admission.

Each effect has a phase timeout and the operation has a total deadline. Kamal
inherits the controller's actual locked file descriptor; its adapter verifies
the inode and flock before bypassing the separate persistent Kamal lock. A child
still running after a controller crash retains that exclusion; its independent
deadline watchdog terminates its local process group when the phase expires.
Remote SSH command termination is not inferred from local process exit; shared-home
ownership and startup admission continue to fence late remote effects. An abandoned
provisional application lease closes admission and stops workers. Fresh process
startup always remains closed until explicitly admitted.

An interrupted operation cannot be resumed by `run`. Explicit `recover` performs
the same closure and verification sequence using the compatible predecessor.
After a durable publication commit, recovery reconciles the committed release
instead. This rule survives another interruption during reconciliation. No path
rewinds database or file state. Writes from authorized worker startup or a briefly
published candidate remain present on rollback.

A failed graceful drain, unavailable retained image, altered input, incompatible
release or unhealthy dependency leaves maintenance unresolved. Do not delete the
journal, use `docker kill`, run ordinary `kamal deploy`, or restore old database
state to make a handoff appear successful. Diagnose the failing phase and retain
the same operation evidence. A controller reboot also requires explicit recovery;
this code does not autonomously approve work after a machine restart.

## Validation and remaining qualification

`task managed:kamal:check` exercises both the normal scaffold and this explicit
adapter against the locked gem. The Go suites cover the durable coordinator,
cross-controller exclusion, compatibility checks, Unix control protocol,
startup-closed HTTP, SSE/request draining, database privilege changes and loss of
the controller during provisional admission. After proxy recreation, the
controller waits for verified HTTPS readiness within the existing phase budget;
Docker's detached startup is not itself evidence that the proxy is ready.

`task managed:kamal:transport-test` runs the pinned Kamal adapter over real
loopback SSH against a dedicated Docker daemon and kamal-proxy. It requires Nix
and root (or sudo) on Linux. The runner verifies fresh mount, network and PID
namespaces before hiding the host runtime, root home and application storage.
Keep the checkout, artifacts and evidence outside `/root`, `/run` and `/var`;
the fixture replaces these directories privately and provisions its own SSH
privilege-separation directory, independent of the host distribution.
It builds two synthetic protocol images, preloads the digest-pinned proxy and
locked gems, then stops its local fixture registry before exercising the
candidate and predecessor. Public probes use a separate network namespace and
verify the generated certificate. The exercise checks private ingress, proxy
route restoration, retained-image boot, clean process stop and preservation of
a file write acknowledged through the candidate proxy. Evidence is written to
`.tmp/kamal-transport/transport.json` and retained by managed-scaffold CI.

This is **transport-only evidence**: the synthetic server is not LeapView and
no artifact-admission records are issued. It does not qualify application
database behavior, actual workers, SSE, customer uploads, the complete Go
maintenance coordinator, ACME renewal or provider recovery. The custom
certificate fixture leaves normal certificate verification enabled. Full
application qualification requires two admitted images supporting the private
maintenance protocol and the actual application/database workload. Run
`task ci` before promotion.

Remaining D11 work includes qualified migration-authority coordination for
schema-changing releases, capacity/retention evidence, and a real dedicated host
run covering update, failure before/after publication, rollback after acknowledged
writes, reboot, TLS renewal, uploads and public SSE reconnects. This command
rejects schema-changing requests. D12 owns credential activation/rotation/recovery;
D13 owns coordinated data/provider recovery; D14 owns production observation and
retirement.
