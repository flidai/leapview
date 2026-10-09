# Fresh disk installation

The operator selects `leapview.bootMode` after inspecting the actual rescue
environment and target disk. `bios` remains the default and uses an EF02 GRUB
partition. `uefi` creates a 512 MiB FAT32 ESP mounted at `/boot` and installs
GRUB's removable EFI path without changing firmware variables. The root
filesystem remains ext4. Disko formatting is an initial-install operation;
deploy-rs updates never run it.

`task managed:hosts:fresh-install-test` uses the reviewed, locked Disko test
harness. Each mode formats an actual blank disposable target disk, mounts it,
installs the bootloader through `nixos-enter`, stops the installer, and boots the
installed disk through BIOS or OVMF. Assertions inspect the actual firmware,
partition type, filesystem, bootloader path and persistent data/profile after a
second boot. The existing protected `managed-scaffold.yml` dispatch with
`boot_test=true` runs both modes and retains their logs.

The installed common OS has no public substituters. This proves installation
and repeated boot from already retained local closures while public caches are
unavailable. The upstream harness mounts its test Nix store; it does not prove
a self-contained backup, a cold closure download during an outage, customer
inventory, cloud rescue access, application publication or PostgreSQL recovery.
Those retain their separate qualification and production acceptance gates.
