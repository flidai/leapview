# Troubleshoot LeapView Desktop

Start with the exact message shown by the trusted LeapView window. It describes a safe category without exposing credentials, raw Chromium errors, native error strings, or customer data.

## Cannot verify an instance

| Message category | What to check |
| --- | --- |
| DNS | Confirm the hostname, network, and VPN; then retry. |
| Proxy | Confirm the operating-system proxy or PAC configuration can reach the instance. |
| Certificate | Confirm the complete certificate chain is valid and trusted by the operating-system trust store. There is no bypass. |
| Timeout or network | Confirm the instance and reverse proxy are healthy and reachable from the device. |
| Redirect | Enter the canonical HTTPS origin rather than an alias that redirects elsewhere. |
| Compatibility | Upgrade the server or client so discovery schema, protocol, authentication mode, and capabilities agree. |
| Identity changed | Contact the administrator before accepting replacement; DNS may now point to a different deployment. |

LeapView uses Chromium's platform network stack. A positive “online” signal does not prove that private DNS, VPN, proxy, or the instance itself is reachable; discovery remains the authoritative check.

## Browser authentication did not finish

- Return to LeapView and retry if the browser was closed or the request expired.
- Complete sign-in in the same browser flow that LeapView opened.
- Ask the administrator whether the configured provider is qualified for desktop loopback authentication.
- A callback bind failure, cancellation, duplicate callback, provider rejection, or application restart intentionally invalidates that attempt.

LeapView never reuses the authorization code or silently launches a second sign-in.

## Session expired or access was revoked

A **session expired**, signed-out, or revoked message is expected after the idle or absolute lifetime, an administrator action, or server-side policy change. Reopen the profile and complete a new browser sign-in. Do not clear every profile unless the problem affects all of them.

Authorization failures remain server-owned. Updating or reinstalling the desktop client cannot grant missing project, dashboard, or data permissions.

## A remote window closed or stopped responding

LeapView destroys the failed renderer and creates a fresh hardened window. While visible and online it makes a bounded series of safe discovery and session checks before reopening the last same-origin GET route. It does not reload stale renderer state or replay interrupted commands.

After the retry budget is exhausted, use **Reopen**. If repeated renderer, child-process, or GPU failures continue, capture Diagnostics and contact support.

## Diagnostics

Open **Diagnostics** from the native menu or trusted shell and copy the report. It contains bounded client facts such as:

- application, Electron, Chromium, and operating-system versions;
- profile count and non-secret profile identifiers;
- safe error and recovery categories;
- updater state without feed contents or native error text;
- hardening and package-verification state.

Diagnostics exclude cookies, authorization material, customer URLs and query strings, dashboard data, release notes, HTML, network payloads, and secrets. Review the text before sharing it.

When contacting support, include the application version, operating system and architecture, safe error category, whether the issue survives a restart, and the approximate time. Do not send passwords, cookies, tokens, or confidential dashboard content.
