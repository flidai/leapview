# Linux toolchain callers

Inventory against main `b78a38b1c37c5a2242497ee8f05e1df95790781d`,
1 October 2026. Toolchain adoption does not qualify a new release artifact or
hosting profile. Run `rg -n 'setup-ci|setup-go|setup-node|setup-bun|apt-get' .github`
when adding or removing a caller.

| Caller | Tool owner | Contract and remaining boundary |
| --- | --- | --- |
| `scripts/develop.sh` | Locked root flake on x86_64 Linux | Existing Task ordering; other platforms execute their conventional tools |
| `ci.yml` | Shared `setup-ci`, default Nix on x86_64 Linux | Cumulative PR planner, generated checks, all selected validation lanes and exact-plan gate |
| `merge-validation.yml` | Shared `setup-ci`, default Nix | Existing complete merge-queue candidate checks |
| `nightly.yml` | Shared `setup-ci`, default Nix | Existing full checks, security scans, dependency evidence and diagnostic evaluations |
| `nix-development.yml` | Root flake directly | Native/compiler and real-browser checks, fresh-checkout PR/full/nightly contract selection; image qualification remains independent |
| `demo-upgrade-qualification.yml`, `recovery-evidence-qualification.yml` | Shared `setup-ci`, default Nix | Existing disposable recovery/transition fixtures, without live host adoption |
| `security.yml`, `dbt-warehouse-boundary-reference.yml`, `dbt-warehouse-boundary-azure-qualification.yml` | Shared `setup-ci`, default Nix | dbt's pinned Python environment remains separate; cloud qualification retains its own gates |
| `demo-deploy.yml` | Selected source's shared setup action | Historical source revisions retain their own action; current revisions use default Nix tools. Demo remains Compose |
| `artifacts.yml` installed-candidate qualification | Shared `setup-ci`, default Nix | Qualifies conventional immutable images; populates default-branch validation caches |
| `release.yml` | Explicit `toolchain: conventional` | Published CLI/application archives and exact-image qualification retain existing platform/loader baselines pending independent Stage 2 adoption |
| `desktop-preview-candidate` action, `desktop-preview-release.yml`, `electron-security-proof.yml` | Conventional native Go/Bun/Electron builders | Retain Linux packaging and supported macOS/Windows paths until desktop artifact qualification |
| `oci-admission` action | Existing conventional Go verifier | Independent protected final-artifact admission; no Nix output adoption implied |
| `ci-health.yml`, `localdocker-macos.yml`, `site-kamal-trial.yml`, `public-site-smoke.yml` | Existing purpose-specific setup | Preserve health reporting, non-Linux tests and separately qualified site operations |
| `managed-scaffold.yml` | Locked managed-host flake, pinned OpenTofu installer and Ruby container | Separate host closure/activation/network checks, mocked infrastructure and Kamal template contracts; the root development shell does not replace these role-specific inputs |
| `hetzner-deploy.yml`, `site-infrastructure.yml` | Existing pinned Terraform installer | Retain infrastructure state ownership and provider validation independently of application tools |
| Terraform setup in `setup-ci` | Existing pinned Terraform 1.13.5 installer and provider cache | Required by existing deployment validation; do not replace infrastructure state ownership during a tools change |
| `scripts/promtool.sh` | Existing checksum-pinned platform download | Existing rules/fixture contracts remain unchanged until a Nix replacement is independently verified |

The Nix shell supplies Go, Bun, Node, Task, C/C++ compiler, pkg-config,
PostgreSQL utilities, Docker/Compose client, native libraries, browser and fonts.
Task owns source generation, package-manager locks, fixture lifecycle and test
ordering. Docker remains an external running service.

The conventional setup branches remain necessary for release and non-Linux
callers. Remove an installer only after every caller using that branch has a
qualified replacement. Keep archive compatibility, image security, hosting
recovery and profile observation evidence separate from this toolchain contract.
