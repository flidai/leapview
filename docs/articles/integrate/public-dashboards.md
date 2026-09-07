# Public and embedded dashboards

Dashboard publications expose one compiled dashboard as an anonymous, governed read surface. Each publication has a stable standalone URL and an iframe URL. Publishing does not create an API credential, inherit the deployer's permissions, or make arbitrary semantic queries available.

## Configure a publication

Dashboard publication state belongs to the target control plane, not the portable analytics source root. Ask a target administrator to configure the publication for the exact compiled dashboard and default page, then use **Admin → Publications** or the dashboard-publication API to inspect and operate it.

Origins are exact. Internet origins require HTTPS, and wildcards, credentials, paths, queries, and fragments are rejected. An empty list permits the standalone URL but denies framing.

Publish the source bundle to production before enabling the target-owned publication. The public ID remains stable across later deployments and follows the active production generation.

## Govern anonymous data

Public execution uses a credential-less `dashboard_publication` principal scoped to the compiled dashboard execution manifest. Global data policies continue to apply. When two public presentations need different rows or masks, create a publication-specific data policy through **Admin → Access** or the [Access API](/docs/api/access). The policy must target the exact semantic-model resource and publication subject retained by the target.

Publication subjects are supported only by data policies, not role bindings or grants. They cannot authenticate, hold tokens, use agents, export data, edit dashboards, or call the general BI API.

## Operate publications

Users with `RESOURCE_PUBLISH` on the governed dashboard can use **Admin → Publications** or the dashboard-publication API to copy URLs, inspect origins and history, suspend, resume, or rotate a public ID. Project administrators and other principals with this dashboard capability can manage publications.

- Suspension immediately makes documents and commands unavailable and terminates active streams.
- Resume succeeds only while the publication remains in the active production configuration.
- Rotation invalidates the prior URL and active streams immediately.
- Removing or disabling the target configuration makes the publication unavailable while preserving its audit history.

Mutation API requests require an `Idempotency-Key`.

## Verify the publication

Open the standalone URL in a private browser session and confirm the dashboard loads, page navigation works, and no application or agent controls appear. Embed the iframe from an allowed origin and exercise filters, selections, and table windows. A parent origin not listed in `allowedOrigins` must be rejected by browser CSP.

Suspend the publication from **Admin → Publications** and confirm the document becomes unavailable and any active stream closes. Resume it before sharing the URL. Use rotation only when the old URL must stop working immediately.

## Embed safely

Use the generated iframe snippet. Keep `sandbox="allow-scripts allow-same-origin"` and `referrerpolicy="no-referrer"`. The iframe document has no authenticated application chrome or cookies, and browser CSP verifies that the parent origin is explicitly allowed.

Public IDs reduce accidental discovery but are not secrets. Apply data policies as though anyone with the URL can view the publication, monitor public query and rate-limit telemetry, and rotate the URL if it is distributed unintentionally.
