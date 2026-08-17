# Deploy and operate LeapView

LeapView separates application delivery from project delivery. Choose the workflow that matches what is changing, then use the focused operational guide for execution and verification.

## Deliver the application

Application delivery changes the LeapView binary or image, browser assets, runtime configuration, infrastructure, and persistent storage attachment.

- Use [Production configuration](/docs/guides/operate/production-configuration) to establish secrets, public addresses, storage, and capacity boundaries.
- Use [Self-hosting](/docs/guides/operate/self-hosting) to deploy the supported single-node topology.
- Use [Upgrades and migrations](/docs/guides/operate/upgrades) to rehearse, apply, verify, and roll back an application release.

Application releases should use immutable artifacts and should not silently modify project resources.

## Deliver a project

Project delivery changes connections, sources, model tables, semantic models, pipelines, dashboards, access declarations, and managed-data revision pins.

- Use [Develop, review, and publish](/docs/cli/validate-deploy) for the exact-candidate delivery workflow.
- Use [Targets and environments](/docs/cli/targets) to keep local, staging, and production identities explicit.
- Read [Projects and environments](/docs/concepts/projects-environments) for the ownership and activation model behind that workflow.

Promote the same reviewed project commit and managed revision identities through environments rather than maintaining separate dashboard trees.

## Run and recover the service

- Use [Health and observability](/docs/guides/operate/observability) for readiness, metrics, logs, refresh activity, query events, and synthetic verification.
- Use [Backup and restore](/docs/guides/operate/backup-restore) for consistent instance recovery.
- Use [Operational troubleshooting](/docs/guides/operate/troubleshooting) to locate failures across infrastructure, active data, semantic modeling, and dashboard behavior.
- Use [Storage and recovery](/docs/guides/data/storage-recovery) for local and object-storage analytical boundaries.

For accepted settings and flags, use the generated [Environment variable reference](/docs/configuration) and [CLI command reference](/docs/cli/reference).
