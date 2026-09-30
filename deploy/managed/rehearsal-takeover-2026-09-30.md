# Managed scaffold takeover validation, 2026-09-30

Status: local scaffold validation; production qualification remains incomplete.

## Tested source and inputs

- Continuation implementation: `9a7ac4ffff98fad0d7bd87c2d9e2489e695f6365`.
- Retained native-fixture implementation: `4a6ac2c102071ee6af96993624bc9d8178161d36`.
- Preserved upstream PR merge: `d51bb364983e177066f3abdd00a2c6e153f02806`.
- Main: `ac1433754574d9f210c2705f3ba207aabbc097cf`.
- Initial managed NixOS source tree: `ed77c9fe043c0054e6f6dfe78d594fa8e1789a29`.
- Final managed NixOS source tree: `95ba43eaf0d1dcf544c253b83928fb601b714e07`.
  The subsequent revision changes validation assets and flake exports; the
  host-role modules remain unchanged.
- Host lockfile SHA-256:
  `0921683533542477d6353c0ea874a630939d05dd516dded9a5b997fbbb36c550`.
- nixpkgs: `cf5e76507c6e23b59f7e0ffcc7baa2a39ddd8442`.
- Tools: Nix 2.31.2, OpenTofu 1.12.6, local Ruby 3.3.8, locked Kamal 2.12.0.
  Hosted template validation retains its pinned Ruby 3.4 container.

The initial local main merge and the concurrent upstream main merge had identical
trees. The continuation was rebased onto the upstream merge without changing its
tested tree. No force push or replacement of the upstream author's work was used.
Full repository CI covers the continuation revision. After adding the native
fixture, host builds/formatting, the native test and Go CI/architecture contracts
were rerun against the fixture revision. Application Go and frontend sources did
not change between those revisions. The guest attempts cover the initial tree.

## Results

| Check | Result |
| --- | --- |
| OpenTofu formatting, initialization with read-only provider lock and validation | Passed |
| Five mocked two-host infrastructure plans | Passed |
| Nix formatting | Passed |
| Complete app and database role closures | Passed |
| Host assertions, deploy-rs activation paths and deployment schema | Passed |
| Native Kamal parser/command tests | Four tests, 23 assertions passed |
| Focused Go CI-policy and public Compose contracts | Passed |
| Shell syntax and Python compilation | Passed |
| Isolated kernel ingress regression | Two passes covering IPv4/IPv6 public original ports 80/443, denied public 8080/9000, denied private ingress, outbound requests/responses and policy reapplication |
| Isolated real-Docker regression | Passed with locked Docker 29.8.0: custom bridge, public original-port allowlist, public/private bypass denial, policy reapplication, daemon restart with container restoration and outbound responses |
| Full `task ci` on the prepared checkout | Passed in the serial run, including PostgreSQL conformance, frontend shards and final generated-contract verification |
| NixOS guest and reboot fixture | Unverified: host OOM killed the app QEMU process during image loading; ingress/reboot assertions were not completed |

The kernel reproduction first failed against the original rules because public
8080 was reachable. The fixed policy also denies a container's port 80 when it is
published as host port 9000; an allowed original host port 80 can reach a translated
container port 8080. The committed test runs in new network and mount namespaces,
without changing the existing host's firewall or Docker daemon.

Built host identities:

```text
/nix/store/l6hz6fqdp0grkjwhjylq2y1c4k853pwq-nixos-system-example-app-26.05.20260927.cf5e765
/nix/store/jl8adn49ix5m6ai8v9vq475wcbdwbqaq-nixos-system-example-database-26.05.20260927.cf5e765
```

The committed `task managed:hosts:docker-test` uses the host profile's exact
Docker package `/nix/store/q801cdid561shx2159agwqsnbc0kxrif-docker-29.8.0` and a
locally built probe archive with SHA-256
`7b8c7dbd2be86f15d0efd3c99e91a12ab5292831ed4dc2c11708acd9e7956e81`.
The execution kernel was `7.0.0-29-generic`. It runs in fresh mount/network/PID
namespaces, uses a private empty daemon configuration and unique temporary state,
and cleans up its daemon, container and data. The existing host daemon and network
are outside that fixture. This checks the pinned engine's forwarding behavior;
it does not establish NixOS boot or deploy-rs failed-update recovery.

## Execution limits and recovery

The first `task ci` invocation populated ignored generated outputs that were
absent in this fresh worktree, so its initial generated-snapshot check failed.
The contract was restarted after generation; no tracked generated contracts were
changed to suppress the check.

A concurrent guest run caused four PostgreSQL application tests to time out
opening their pools. All four passed in the subsequent serial run, as did the
complete CI contract. Guest processes were stopped before that rerun; no
application/authentication code or test deadlines were changed.

The shared Linux runner has no KVM device. Initial unconstrained Nix evaluation
and a 2 GiB-per-guest run were killed under memory pressure. Host builds now use
separate role evaluations, one build job and two build cores. The guest fixture
uses 1 GiB for each service host and 256 MiB for the outsider; these are fixture
budgets, not customer capacity recommendations. A standalone compiled-driver run
then activated the firewall and started Docker, but the host OOM killer terminated
the application QEMU process during image loading. The new guest ingress/reboot
assertions therefore remain unverified. The emulated network-online unit also
timed out, as in the earlier rehearsal; real network convergence is unqualified.
No all-units-healthy host claim is made. The smaller kernel and real-Docker
regressions passed and are retained as routine CI checks.

No Hetzner resources, customer inventory, production keyrings, bucket credentials,
live certificates or existing servers were changed. Guest-generated credentials
are disposable. The test does not exercise Disko installation, firmware boot,
Hetzner DHCP/interface naming, actual Tailscale grants, deploy-rs lost-connectivity
recovery, off-host backups or a protected first publication. Previous component
and application rehearsals retain their original source identities and limits.

Successful build/network checks do not establish production readiness. Follow the
[completion plan](completion-plan.md) for the remaining Stage 3–6 qualification
and keep Nix release compatibility/security admission independent.
