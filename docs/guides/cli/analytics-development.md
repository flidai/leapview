# Analytics development workflow

Use this guide when changing a dashboard, model, or semantic definition. It
describes the local-first workflow being assembled on the current branch and
calls out the smaller boundary currently covered by a public release.

## Read the release boundary first

The current branch contains implementation and local tests for `init`, the
versioned local runtime, verified Docker selection, resumable bootstrap, shared
local lifecycle, strict source profiles, profile application, credential
rotation/retirement, pinned preview state, and guided deployment operation
descriptors. Those tests are evidence for the checkout; they are not a
released-package qualification result.

The qualification harness defines the boundary for a future or exact released
authoring archive: it checks archive integrity, package/runtime identity, the
executable version, and read-only command help. Its optional lifecycle requires
the exact archive under test, a supported Linux or macOS host, an explicit
local Unix Docker socket, Compose 2.17 or newer, and possibly a human browser
approval. The current public archives are recorded as Compose/`leapviewctl`
archives; the installable authoring CLI archive remains release-blocked until
FAI-798 ships. Preview edit-to-visible measurements and production deploy
qualification remain `not-run`/planned in the
[qualification contract](../../../deploy/local/qualification/qualification-contract.json).
Do not treat the qualification directory as evidence that a released authoring package currently exists.
The [CLI contract](../../../adr/specifications/analytics-development-cli-contract.md)
is the intended interface, not a claim that every row is released.

## Migrate from contributor development

`task dev` remains the contributor workflow for changing LeapView itself. It
expects the source checkout and contributor toolchain, builds the application,
provisions contributor fixtures, and may enable diagnostics such as the
Datastar inspector. It is not the installation or analytics-authoring path.

For analytics authoring, install the versioned archive when an exact archive is
available, and keep its `local-runtime` sibling next to the executable. The
archive install and host requirements are in [Install the authoring CLI](../../../deploy/local/INSTALL.md).
Use the generated [`init`](/docs/cli/init), [`dev`](/docs/cli/dev), and
[`deploy`](/docs/cli/deploy) references for the exact flags of the build you
are running.

## Create or open a project

Start a new checkout with a bounded, synthetic example:

```sh
leapview init my-analytics
cd my-analytics
```

`init` creates the conventional `dashboards/` resource tree, small sample
data, and a development-input manifest. It does not download production data,
copy production credentials, or create a production target. Existing
checkouts can skip `init`; keep their source root and target configuration
unchanged.

## Run local development

Bare `dev` means local development. It must not be redirected by
`LEAPVIEW_TARGET`, a saved production login, or a Docker context named
`default`:

```sh
leapview dev
```

The launcher verifies and pins the effective local Docker endpoint before any
pull, Compose mutation, or data staging. Use an explicit supported local
socket/context when needed, and use `--no-browser` to print the private
preview URL instead. SSH endpoints, arbitrary TCP endpoints, and forwarding
tunnels are not local-runtime proof. A context or daemon change during
startup must fail closed rather than redirecting a later operation.

The local runtime owns checkout-scoped Compose services and durable volumes.
For the generated sample, `dev` verifies every input declared in
`.leapview/development-inputs.yaml`, plans the exact immutable revisions, and
stages them to the internally resolved local target and Project before the
first candidate synchronization. The staging operation is resumable and
idempotent, so a restart reuses the retained revision without asking the author
to copy a URL or internal Project identifier.

Only declared bounded synthetic fixtures may be staged automatically. A source
save watches authored files and sends a coherent candidate through the normal
candidate APIs; it never mounts live YAML as serving state or stages/refreshes
data. Fixture changes require a matching manifest update and a `dev` restart;
ordinary YAML edits never refresh mutable inputs.
Invalid edits retain the last valid candidate and report actionable
diagnostics. A candidate transition pins all queries in a view to one
candidate/snapshot identity, so late responses from an older view cannot mix
with the replacement.

## Add a strict source profile

External inputs are configured separately from portable analytics source. The
default file is `.leapview/profiles.local.yaml`; it is selected with
`--profile-file` and one named profile with `--profile` (default `local`). A
profile is a complete description of required target-bound connections, not a
merge over retained bindings:

```yaml
version: 1

profiles:
  local:
    connections:
      commerce:
        endpoint:
          host: analytics-reader.example.com
          port: 5432
          database: commerce
          tlsMode: verify-full
        credentials:
          env: LEAPVIEW_DEV_CONNECTION_COMMERCE
```

The name must resolve to the exact logical `Connection` in the compiled graph.
Unknown fields, duplicate keys, unsupported versions/options, inline secrets,
and secret-bearing URLs fail before binding application or upstream access.
Credential bundles remain in the allowlisted environment reference; they are
not interpolated into YAML or forwarded wholesale from the host. A fixture-only
project may omit the file. Missing required coverage or credentials is an
actionable failure, never a fallback to a retained binding or production
credential.

Before the first profile connection test or approved upstream read, review the
redacted source/endpoint summary and pass explicit consent:

```sh
leapview dev --allow-upstream-read
```

Use developer-specific, least-privilege (preferably read-only) upstream
credentials. The consent boundary distinguishes live reads from refresh/build
work that retains local results. Saving YAML does not silently refresh mutable
upstream data, and a local profile does not promise offline operation.

## Use remote development explicitly

Remote development is an application connection to an already authorized
LeapView target. It starts no local containers and cannot be combined with
local profile flags:

```sh
leapview dev --target staging
```

Bare `dev` and `dev --target staging` therefore have different ownership and
failure boundaries. Remote mode is useful for private-network data or a
shared QA target; it is not permission to provision a remote Docker host.

## Inspect and stop the local runtime

Use the checkout-scoped lifecycle commands:

```sh
leapview dev status
leapview dev logs --tail 200
leapview dev stop
leapview dev reset                 # prints the exact state and confirmation
leapview dev reset --confirm sha256:<printed-value>
```

Each attachment has independent liveness. Ctrl-C detaches only that session
while another live attachment remains; the last detach stops managed services
and retains volumes. `stop` and `reset` refuse without mutation while a live
attachment remains. `stop` retains data; `reset` removes only the confirmed,
checkout-owned resources after rechecking endpoint, ownership, and liveness.

## Deliver with guided deploy

Production owns the target's bindings, credentials, grants, managed-data
revisions, qualification, approval, and activation. A local profile or fixture
is never promoted. Deployment sends the portable analytics source; production
resolves its own bindings and credentials.

Interactive deploy starts a new operation when there is no unresolved work:

```sh
leapview deploy --target prod --project-id PROJECT_ID --environment prod
```

If retained work exists, the command shows it and requires an explicit choice.
Use `--new` for a fresh source snapshot or `--resume --operation HANDLE` for a
retained operation:

```sh
leapview deploy --target prod --project-id PROJECT_ID --environment prod \
  --new --operation release-42

leapview deploy --target prod --project-id PROJECT_ID --environment prod \
  --resume --operation release-42
```

Review the exact target, Project/environment, source digest, plan digest, and
impact/physical-work evidence before build/publication. Noninteractive calls
must choose exactly one intent and a handle; `--new` and `--resume` are
mutually exclusive. Only target-confirmed activation is an active success.
Pending approval, rejected, and indeterminate outcomes remain distinct.

### Recover an operation artifact

The operation descriptor is a credential-free, versioned local/CI handoff. It
retains the immutable source snapshot, target identity, plan/build/publication
identities, idempotency keys, and known outcome; credentials stay in the
target/client credential mechanism. Preserve the descriptor as a CI artifact
when later steps use another checkout or machine.

On resume, the retained source snapshot is authoritative. Changed working-tree
files are not silently included, a sealed candidate is not rebuilt, and a
missing or mismatched descriptor is a structured selection failure. Reconcile
an acknowledgement that may have succeeded before retrying publication. If
the descriptor cannot be recovered, stop and choose an explicit new operation
after reviewing the target's unresolved state; do not reconstruct it from the
current files or select the newest operation implicitly.

## Optional dbt importer

The optional dbt profile importer is deferred. Initial profile conformance
uses the strict native YAML above. A future importer may translate one
explicitly selected, supported dbt target after preview and confirmation; it
will not establish general `profiles.yml` compatibility, execute arbitrary
expressions, or push credentials to production.

Continue with [Targets and environments](/docs/cli/targets),
[Develop, review, and publish](/docs/cli/validate-deploy), and
[Automation and CI](/docs/cli/automation). For implementation/release status,
see the [Milestone 5 conformance evidence matrix](../../../adr/specifications/adr-0021-milestone-5-conformance-evidence.md).
