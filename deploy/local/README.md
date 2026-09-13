# Local analytics runtime payload

This directory is the versioned runtime payload consumed by the released
`leapview dev` lifecycle. It is not the contributor workflow (`task dev`) and
is not a supported manual Compose onboarding recipe.

The v1 payload requires Docker Compose 2.17.0 or newer because dependency
updates must restart the network-namespace-sharing application service. The
release-generated `runtime-package.json` records this minimum, the persistent
state schema, the exact LeapView release/image identity, and the pinned
PostgreSQL major/image. A lifecycle controller must reject incompatible or
unknown manifest and persistent-state versions; startup must never perform an
implicit state upgrade.

The package contains exactly two services: the version-matched LeapView image
and pinned PostgreSQL 18. PostgreSQL initializes the separate
`leapview_control` and `leapview_ducklake` databases through the canonical
`deploy/postgres/init.sh` role contract. A PostgreSQL volume and a private
LeapView volume retain the checkout's database state, analytical files,
managed objects, immutable source and serving artifacts, DuckDB state, and
runtime artifacts.

The application joins PostgreSQL's network namespace. PostgreSQL listens only
on that namespace's `127.0.0.1`, so its development-only, TLS-disabled database
connections remain loopback-only. The application uses LeapView's existing
local browser-authentication authority; it does not enable the development auth
bypass. Only the selected application port is published to host loopback. The
package does not publish PostgreSQL, mount analytics source, forward the host
environment, or include an external database, object store, secret provider,
or warehouse.

The host lifecycle controller is responsible for all mutation. Before reading
or applying this payload it must:

1. verify and pin a supported local Docker endpoint;
2. derive a canonical checkout-scoped Compose project and ownership identity;
3. verify that the release manifest, host CLI, and immutable LeapView image
   identify the same release;
4. create a private mode-0600 environment with generated local credentials;
5. invoke the existing migration, instance/Project, physical-pool admission,
   and authentication authorities; and
6. reconcile health and retained state without silently upgrading persistent
   storage.

The released UI uses production-built assets but development serving policy.
Contributor diagnostics, including the Datastar inspector, remain disabled
unless the source-only contributor workflow explicitly enables them.

`dev stop` and `dev reset` ownership, attachment, and liveness rules belong to
the lifecycle controller. The labels in this payload are evidence inputs for
those checks; labels alone never authorize mutation. Local volumes are durable
development state, not a production backup or recovery mechanism.
