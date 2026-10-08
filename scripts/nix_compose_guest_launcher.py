#!/usr/bin/env python3
"""Launch a disposable, native-ISA cloud-init guest for Compose qualification."""

from __future__ import annotations

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import socket
import stat
import subprocess
import sys
import tempfile
import time


ARCHITECTURES = {"amd64": "x86_64", "arm64": "aarch64"}
GUEST_OSES = {"ubuntu2404", "debian13"}
VIRTUALIZATION_MODES = {"kvm", "tcg"}
NONCE_RE = re.compile(r"^[0-9a-f]{32,128}$")
HEX_SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
MANIFEST_PATH = "/etc/leapview-qualification-guest.json"
MAX_QEMU_OUTPUT = 1024 * 1024
MAX_GUEST_RECEIPT_BYTES = 2 * 1024**2
COLLECTOR_OWNED_OPTIONS = (
    "--host", "--port", "--ssh-identity", "--known-hosts", "--nonce", "--guest-manifest",
    "--guest-os", "--virtualization-mode", "--launcher-receipt", "--output-dir",
)


class LauncherError(ValueError):
    """Guest launch or retained evidence is invalid."""


def _digest(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def _file_digest(path: Path) -> str:
    digest = hashlib.sha256()
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path, flags)
    if not stat.S_ISREG(os.fstat(descriptor).st_mode):
        os.close(descriptor)
        raise LauncherError(f"{path.name} is not a regular file")
    with os.fdopen(descriptor, "rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return "sha256:" + digest.hexdigest()


def _canonical(value: object) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()


def _write_new(path: Path, data: bytes, mode: int) -> None:
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path, flags, mode)
    try:
        with os.fdopen(descriptor, "wb", closefd=False) as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.fchmod(descriptor, mode)
    finally:
        os.close(descriptor)


def _required_tool(name: str) -> Path:
    tool = shutil.which(name)
    if tool is None:
        raise LauncherError(f"required locked tool {name!r} is not on PATH")
    return Path(tool).resolve(strict=True)


def _run(command: list[str], *, timeout: float = 30, input_bytes: bytes | None = None) -> bytes:
    try:
        completed = subprocess.run(
            command, input=input_bytes, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            timeout=timeout, check=False,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise LauncherError(f"could not run {Path(command[0]).name}: {exc}") from exc
    if completed.returncode != 0:
        detail = completed.stderr[-2048:].decode("utf-8", errors="replace").strip()
        suffix = ": " + detail if detail else ""
        raise LauncherError(f"{Path(command[0]).name} failed ({completed.returncode}){suffix}")
    if len(completed.stdout) > MAX_QEMU_OUTPUT:
        raise LauncherError(f"{Path(command[0]).name} output exceeded its limit")
    return completed.stdout


def _host_architecture() -> str:
    machine = platform.machine().lower()
    aliases = {"x86_64": "x86_64", "amd64": "x86_64", "aarch64": "aarch64", "arm64": "aarch64"}
    try:
        return aliases[machine]
    except KeyError as exc:
        raise LauncherError(f"unsupported native qualification runner architecture {machine!r}") from exc


def _kvm_api_version() -> int | None:
    try:
        descriptor = os.open("/dev/kvm", os.O_RDWR | getattr(os, "O_CLOEXEC", 0))
    except OSError:
        return None
    try:
        return fcntl.ioctl(descriptor, 0xAE00, 0)
    except OSError:
        return None
    finally:
        os.close(descriptor)


def _check_new_regular_output(path: Path, label: str) -> Path:
    path = path.absolute()
    if path.exists() or path.is_symlink():
        raise LauncherError(f"{label} must be a new file")
    parent = path.parent
    if not parent.is_dir() or parent.is_symlink():
        raise LauncherError(f"{label} parent must be an existing non-symlink directory")
    return path


def _check_inputs(args: argparse.Namespace, collector: list[str]) -> tuple[str, str, Path, Path, Path]:
    if not sys.platform.startswith("linux"):
        raise LauncherError("native Linux QEMU qualification requires a Linux runner")
    if args.guest_os not in GUEST_OSES or args.architecture not in ARCHITECTURES:
        raise LauncherError("unsupported guest OS or architecture")
    if args.virtualization_mode not in VIRTUALIZATION_MODES:
        raise LauncherError("virtualization mode must explicitly be kvm or tcg")
    if not HEX_SHA256_RE.fullmatch(args.image_sha256):
        raise LauncherError("--image-sha256 must be 64 lowercase hexadecimal characters")
    if args.readiness_timeout < 60 or args.readiness_timeout > 900:
        raise LauncherError("readiness timeout must be between 60 and 900 seconds")
    if args.timeout < 60 or args.timeout > 7200:
        raise LauncherError("collector timeout must be between 60 and 7200 seconds")

    host_arch = _host_architecture()
    expected_arch = ARCHITECTURES[args.architecture]
    if host_arch != expected_arch:
        raise LauncherError("qualification runner ISA must match the requested guest ISA")
    if args.virtualization_mode == "kvm" and _kvm_api_version() != 12:
        raise LauncherError("KVM selected but /dev/kvm did not pass the API-version probe")
    if (host_arch == "aarch64") != (args.firmware is not None):
        raise LauncherError("aarch64 requires --firmware; amd64 must not select firmware")

    image = args.cloud_image.absolute()
    image_info = image.lstat()
    if not stat.S_ISREG(image_info.st_mode):
        raise LauncherError("cloud image must be a regular file, not a symlink or device")
    actual_image_sha = _file_digest(image)
    if actual_image_sha != "sha256:" + args.image_sha256:
        raise LauncherError("cloud image bytes differ from --image-sha256")

    output_dir = args.output_dir.absolute()
    if output_dir.exists() or output_dir.is_symlink():
        raise LauncherError("collector output directory must be new")
    if not output_dir.parent.is_dir() or output_dir.parent.is_symlink():
        raise LauncherError("collector output parent must be an existing non-symlink directory")
    launcher_receipt = _check_new_regular_output(args.launcher_receipt, "launcher receipt")
    lifecycle_receipt = _check_new_regular_output(args.lifecycle_receipt, "launcher lifecycle receipt")
    if launcher_receipt == lifecycle_receipt:
        raise LauncherError("launcher and lifecycle receipts must be separate files")
    for receipt in (launcher_receipt, lifecycle_receipt):
        try:
            receipt.relative_to(output_dir)
        except ValueError:
            pass
        else:
            raise LauncherError("launcher receipts must be retained outside the collector output directory")

    if not collector:
        raise LauncherError("a collector command must follow --")
    install_modes = [
        collector[index + 1]
        for index, value in enumerate(collector[:-1])
        if value == "--install-mode"
    ]
    if len(install_modes) != 1 or install_modes[0] not in {"bootstrap", "nix-controller"}:
        raise LauncherError("collector command must select exactly one supported --install-mode")
    if any(option in collector or any(item.startswith(option + "=") for item in collector)
           for option in COLLECTOR_OWNED_OPTIONS):
        raise LauncherError("collector command must leave guest transport and output options to the launcher")

    firmware = args.firmware
    if firmware is not None:
        firmware_info = firmware.lstat()
        if not stat.S_ISREG(firmware_info.st_mode):
            raise LauncherError("AArch64 firmware must be a regular file, not a symlink or device")
        if firmware.stat().st_size == 0:
            raise LauncherError("AArch64 firmware file is empty")
    return host_arch, actual_image_sha, output_dir, launcher_receipt, lifecycle_receipt


def _validate_qcow2(qemu_img: Path, image: Path) -> None:
    data = _run([str(qemu_img), "info", "--output=json", str(image)])
    try:
        info = json.loads(data)
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        raise LauncherError("qemu-img returned invalid JSON for the cloud image") from exc
    if not isinstance(info, dict) or info.get("format") != "qcow2":
        raise LauncherError("official cloud image must be a qcow2 file")

    def reject_external(value: object) -> None:
        if isinstance(value, dict):
            for key, child in value.items():
                if key in {"backing-filename", "full-backing-filename", "backing-filename-format", "data-file", "external-data-file"} and child:
                    raise LauncherError("cloud image contains a backing chain or external data reference")
                reject_external(child)
        elif isinstance(value, list):
            for child in value:
                reject_external(child)

    reject_external(info)
    if info.get("encrypted") is True:
        raise LauncherError("encrypted qcow2 cloud images are unsupported")


def _public_key(path: Path) -> bytes:
    try:
        fields = path.read_bytes().strip().split()
    except OSError as exc:
        raise LauncherError(f"cannot read generated SSH public key: {exc}") from exc
    if len(fields) < 2 or fields[0] != b"ssh-ed25519":
        raise LauncherError("ssh-keygen did not produce an Ed25519 public key")
    return b"ssh-ed25519 " + fields[1]


def _cloud_config(*, client_public_key: bytes, host_private_key: bytes, host_public_key: bytes,
                  manifest_bytes: bytes) -> bytes:
    cloud_config = {
        "disable_root": False,
        "ssh_pwauth": False,
        "ssh_deletekeys": True,
        "growpart": {"mode": "auto", "devices": ["/"]},
        "resize_rootfs": True,
        "ssh_genkeytypes": ["ed25519"],
        "ssh_keys": {
            "ed25519_private": host_private_key.decode("ascii"),
            "ed25519_public": host_public_key.decode("ascii"),
        },
        "users": [
            "default",
            {
                "name": "root",
                "lock_passwd": True,
                "ssh_authorized_keys": [client_public_key.decode("ascii")],
            },
        ],
        "write_files": [
            {
                "path": MANIFEST_PATH,
                "owner": "root:root",
                "permissions": "0644",
                "content": manifest_bytes.decode("ascii"),
            },
            {
                "path": "/etc/ssh/sshd_config.d/00-leapview-qualification.conf",
                "owner": "root:root",
                "permissions": "0600",
                "content": "PermitRootLogin prohibit-password\nPasswordAuthentication no\nPubkeyAuthentication yes\nHostKeyAlgorithms ssh-ed25519\n",
            },
        ],
        "runcmd": [["systemctl", "restart", "ssh"]],
    }
    return b"#cloud-config\n" + _canonical(cloud_config)


def _ssh_command(*, port: int, identity: Path, known_hosts: Path, command: str) -> list[str]:
    return [
        "ssh", "-F", "/dev/null", "-p", str(port), "-i", str(identity),
        "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
        "-o", "HostKeyAlgorithms=ssh-ed25519", "-o", "UserKnownHostsFile=" + str(known_hosts),
        "-o", "GlobalKnownHostsFile=/dev/null", "-o", "UpdateHostKeys=no",
        "-o", "ClearAllForwardings=yes", "-o", "ForwardAgent=no",
        "-o", "PermitLocalCommand=no", "-o", "ConnectTimeout=5", "root@127.0.0.1", command,
    ]


def _wait_ready(*, process, port: int, identity: Path, known_hosts: Path,
                manifest_sha256: str, timeout: int) -> None:
    command = (
        "cloud-init status --wait >/dev/null && "
        "sha256sum " + MANIFEST_PATH + " | grep -q '^" + manifest_sha256.removeprefix("sha256:") + "[[:space:]]'"
    )
    deadline = time.monotonic() + timeout
    last_probe = "SSH readiness command was not attempted"
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise LauncherError("QEMU exited before cloud-init and the guest manifest became ready")
        completed = None
        try:
            completed = subprocess.run(
                _ssh_command(port=port, identity=identity, known_hosts=known_hosts, command=command),
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                timeout=max(1, deadline - time.monotonic()), check=False,
            )
        except subprocess.TimeoutExpired:
            last_probe = "SSH readiness command timed out"
        except OSError:
            last_probe = "SSH readiness command could not run (OS error)"
        except subprocess.SubprocessError:
            last_probe = "SSH readiness command could not run (subprocess error)"
        else:
            last_probe = f"SSH readiness command exited {completed.returncode}"
        if completed is not None and completed.returncode == 0:
            return
        time.sleep(min(5, max(0, deadline - time.monotonic())))
    raise LauncherError("guest readiness timed out before cloud-init completed (" + last_probe + ")")


def _terminate(process) -> bool:
    if process is None:
        return True
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=10)
    return process.poll() is not None


def _append_collector_args(collector: list[str], *, port: int, identity: Path, known_hosts: Path,
                           nonce: str, manifest: Path, guest_os: str, mode: str,
                           launcher_receipt: Path, output_dir: Path) -> list[str]:
    result = list(collector)
    options = {
        "--host": "127.0.0.1",
        "--port": str(port),
        "--ssh-identity": str(identity),
        "--known-hosts": str(known_hosts),
        "--nonce": nonce,
        "--guest-manifest": str(manifest),
        "--guest-os": guest_os,
        "--virtualization-mode": mode,
        "--launcher-receipt": str(launcher_receipt),
        "--output-dir": str(output_dir),
    }
    for option, value in options.items():
        result.extend((option, value))
    return result


def _qemu_command(*, binary: Path, seed: Path, overlay: Path, port: int,
                  architecture: str, mode: str, firmware: Path | None, serial_log: Path) -> list[str]:
    if architecture == "x86_64":
        machine = "q35"
        cpu = "host" if mode == "kvm" else "max"
    else:
        machine = "virt"
        cpu = "host" if mode == "kvm" else "max"
    command = [
        str(binary), "-machine", f"{machine},accel={mode}", "-cpu", cpu,
        "-m", "4096", "-smp", "2", "-display", "none", "-serial", "file:" + str(serial_log),
        "-monitor", "none",
        "-drive", f"file={overlay},if=virtio,format=qcow2",
        "-drive", f"file={seed},if=virtio,format=raw,readonly=on",
        "-netdev", f"user,id=qualification,hostfwd=tcp:127.0.0.1:{port}-:22",
        "-device", "virtio-net-pci,netdev=qualification",
    ]
    if firmware is not None:
        command.extend(("-bios", str(firmware)))
    return command


def _build_manifest(*, nonce: str, image_sha256: str, guest_os: str, architecture: str, mode: str) -> bytes:
    return _canonical({
        "schemaVersion": 1,
        "nonce": nonce,
        "sourceCloudImageSHA256": image_sha256,
        "guestOS": guest_os,
        "architecture": architecture,
        "virtualizationMode": mode,
    })


def _write_lifecycle(path: Path, value: dict) -> None:
    _write_new(path, (json.dumps(value, indent=2, sort_keys=True) + "\n").encode(), 0o600)


def launch(args: argparse.Namespace, collector: list[str]) -> dict:
    phase = "preflight"
    qemu = None
    temporary = None
    temp_path = None
    ready_receipt_sha = None
    guest_receipt_sha = None
    collector_exit_code = None
    qemu_terminated = False
    temp_removed = True
    result = "failed"
    failure = None
    lifecycle_path = args.lifecycle_receipt.absolute()
    launcher_path = args.launcher_receipt.absolute()
    output_dir = args.output_dir.absolute()
    lifecycle_writable = False

    try:
        lifecycle_path = _check_new_regular_output(args.lifecycle_receipt, "launcher lifecycle receipt")
        lifecycle_writable = True
        launcher_path = _check_new_regular_output(args.launcher_receipt, "launcher receipt")
        if launcher_path == lifecycle_path:
            raise LauncherError("launcher and lifecycle receipts must be separate files")
        host_arch, image_sha, output_dir, launcher_path, lifecycle_path = _check_inputs(args, collector)
        mode = args.virtualization_mode
        nonce = secrets.token_hex(32)
        if not NONCE_RE.fullmatch(nonce):
            raise LauncherError("nonce must contain 32–128 lowercase hexadecimal characters")

        qemu_system = _required_tool("qemu-system-" + host_arch)
        qemu_img = _required_tool("qemu-img")
        cloud_localds = _required_tool("cloud-localds")
        ssh_keygen = _required_tool("ssh-keygen")
        firmware_sha = None
        if args.firmware is not None:
            firmware_sha = _file_digest(args.firmware)
        qemu_version = _run([str(qemu_system), "--version"])
        phase = "image-validation"
        image = args.cloud_image.absolute()
        _validate_qcow2(qemu_img, image)

        temporary = tempfile.TemporaryDirectory(prefix="leapview-guest-" + nonce[:12] + "-")
        temp_path = Path(temporary.name)
        os.chmod(temp_path, 0o700)
        client_identity = temp_path / "ssh-client"
        host_identity = temp_path / "ssh-host"
        client_public_path = temp_path / "ssh-client.pub"
        host_public_path = temp_path / "ssh-host.pub"
        user_data = temp_path / "user-data"
        meta_data = temp_path / "meta-data"
        manifest_path = temp_path / "guest-manifest.json"
        seed = temp_path / "seed.iso"
        overlay = temp_path / "overlay.qcow2"
        serial_log = temp_path / "serial.log"
        known_hosts = temp_path / "known_hosts"

        phase = "guest-preparation"
        for key_path, comment in ((client_identity, nonce + "-client"), (host_identity, nonce + "-host")):
            _run([str(ssh_keygen), "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", str(key_path)])
        client_public = _public_key(client_public_path)
        host_public = _public_key(host_public_path)
        host_private = host_identity.read_bytes()
        manifest_bytes = _build_manifest(
            nonce=nonce, image_sha256=image_sha, guest_os=args.guest_os,
            architecture=args.architecture, mode=mode,
        )
        manifest_path.write_bytes(manifest_bytes)
        os.chmod(manifest_path, 0o600)
        user_data.write_bytes(_cloud_config(
            client_public_key=client_public, host_private_key=host_private,
            host_public_key=host_public, manifest_bytes=manifest_bytes,
        ))
        os.chmod(user_data, 0o600)
        meta_data.write_bytes(_canonical({"instance-id": "leapview-qualification-" + nonce, "local-hostname": "leapview-qualification"}))
        os.chmod(meta_data, 0o600)
        host_key = host_public.split()
        _run([str(cloud_localds), str(seed), str(user_data), str(meta_data)])
        if not seed.is_file() or seed.stat().st_size == 0:
            raise LauncherError("cloud-localds did not create a seed ISO")
        # Vendor images contain only a minimal root disk. Grow the disposable
        # overlay for package installation, candidate images and authoring builds.
        _run([str(qemu_img), "create", "-f", "qcow2", "-F", "qcow2", "-b", str(image), str(overlay), "40G"])

        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as port_socket:
            port_socket.bind(("127.0.0.1", 0))
            port = port_socket.getsockname()[1]
        if not 1024 <= port <= 65535:
            raise LauncherError("OS selected a non-ephemeral loopback SSH port")
        known_hosts.write_bytes(b"[127.0.0.1]:" + str(port).encode() + b" " + host_key[0] + b" " + host_key[1] + b"\n")
        os.chmod(known_hosts, 0o600)
        qemu_command = _qemu_command(
            binary=qemu_system, seed=seed, overlay=overlay, port=port,
            architecture=host_arch, mode=mode, firmware=args.firmware, serial_log=serial_log,
        )

        phase = "guest-startup"
        serial_file = serial_log.open("wb")
        try:
            qemu = subprocess.Popen(
                qemu_command, stdin=subprocess.DEVNULL, stdout=serial_file, stderr=subprocess.STDOUT,
                start_new_session=True,
            )
        finally:
            serial_file.close()

        phase = "readiness"
        _wait_ready(
            process=qemu, port=port, identity=client_identity, known_hosts=known_hosts,
            manifest_sha256=_digest(manifest_bytes), timeout=args.readiness_timeout,
        )
        inputs = {
            "cloudImageSHA256": image_sha,
            "userDataSHA256": _file_digest(user_data),
            "metaDataSHA256": _file_digest(meta_data),
            "seedISOSHA256": _file_digest(seed),
            "sshClientPublicKeySHA256": _file_digest(client_public_path),
            "sshHostPublicKeySHA256": _file_digest(host_public_path),
            "knownHostsSHA256": _file_digest(known_hosts),
            "firmwareSHA256": firmware_sha,
        }
        launcher_receipt = {
            "schemaVersion": 1,
            "scope": "nix-compose-guest-launcher",
            "result": "ready",
            "nonce": nonce,
            "sourceCloudImageSHA256": image_sha,
            "guestOS": args.guest_os,
            "architecture": args.architecture,
            "virtualizationMode": mode,
            "manifestSHA256": _digest(manifest_bytes),
            "inputs": inputs,
            "runner": {
                "hostArchitecture": host_arch,
                "qemuSystemBinarySHA256": _file_digest(qemu_system),
                "qemuVersionSHA256": _digest(qemu_version),
                "accelerator": mode,
            },
        }
        ready_bytes = (json.dumps(launcher_receipt, indent=2, sort_keys=True) + "\n").encode()
        _write_new(launcher_path, ready_bytes, 0o400)
        ready_receipt_sha = _digest(ready_bytes)

        phase = "collector"
        collector_command = _append_collector_args(
            collector, port=port, identity=client_identity, known_hosts=known_hosts,
            nonce=nonce, manifest=manifest_path, guest_os=args.guest_os, mode=mode,
            launcher_receipt=launcher_path, output_dir=output_dir,
        )
        completed = subprocess.run(collector_command, timeout=args.timeout, check=False)
        collector_exit_code = completed.returncode
        if collector_exit_code != 0:
            raise LauncherError(f"collector failed ({collector_exit_code})")
        guest_receipt = output_dir / "host-guest-receipt.json"
        info = guest_receipt.lstat()
        if not stat.S_ISREG(info.st_mode):
            raise LauncherError("collector did not produce a regular host guest receipt")
        if info.st_size > MAX_GUEST_RECEIPT_BYTES:
            raise LauncherError("collector host guest receipt exceeded its byte limit")
        guest_receipt_sha = _file_digest(guest_receipt)
        result = "passed"
    except Exception as exc:
        failure = exc
    finally:
        try:
            qemu_terminated = _terminate(qemu)
        except Exception as exc:
            qemu_terminated = False
            if failure is None:
                failure = exc
        if temporary is not None:
            try:
                temporary.cleanup()
                temp_removed = temp_path is not None and not temp_path.exists()
            except Exception as exc:
                temp_removed = False
                if failure is None:
                    failure = exc
        if not qemu_terminated and failure is None:
            failure = LauncherError("QEMU could not be terminated")
            phase = "cleanup"
        if not temp_removed and failure is None:
            failure = LauncherError("temporary guest directory could not be removed")
            phase = "cleanup"
        if failure is not None or not qemu_terminated or not temp_removed:
            result = "failed"
        if lifecycle_writable:
            lifecycle = {
                "schemaVersion": 1,
                "scope": "nix-compose-guest-launcher-lifecycle",
                "result": result,
                "failurePhase": None if result == "passed" else phase,
                "collectorExitCode": collector_exit_code,
                "qemuTerminated": qemu_terminated,
                "tempDirectoryRemoved": temp_removed,
                "launcherReceiptSHA256": ready_receipt_sha,
                "guestReceiptSHA256": guest_receipt_sha,
            }
            try:
                _write_lifecycle(lifecycle_path, lifecycle)
            except Exception as exc:
                if failure is None:
                    failure = exc

    if failure is not None:
        if isinstance(failure, LauncherError):
            raise failure
        if isinstance(failure, subprocess.TimeoutExpired):
            raise LauncherError(f"{phase} timed out") from failure
        raise LauncherError(f"{phase} failed: {failure}") from failure
    if result != "passed":
        raise LauncherError(f"{phase} failed")
    return launcher_receipt


def main(argv: list[str] | None = None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    try:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("run", choices=("run",))
        parser.add_argument("--cloud-image", type=Path, required=True)
        parser.add_argument("--image-sha256", required=True, help="expected local image SHA-256 as 64 lowercase hex characters")
        parser.add_argument("--guest-os", choices=sorted(GUEST_OSES), required=True)
        parser.add_argument("--architecture", choices=sorted(ARCHITECTURES), required=True)
        parser.add_argument("--virtualization-mode", choices=sorted(VIRTUALIZATION_MODES), required=True)
        parser.add_argument("--firmware", type=Path)
        parser.add_argument("--readiness-timeout", type=int, default=600)
        parser.add_argument("--timeout", type=int, default=3600, help="bounded collector execution timeout in seconds")
        parser.add_argument("--output-dir", type=Path, required=True)
        parser.add_argument("--launcher-receipt", type=Path, required=True)
        parser.add_argument("--lifecycle-receipt", type=Path, required=True)
        if "--" not in argv:
            parser.parse_args(argv)
            raise LauncherError("collector command must be separated by --")
        separator = argv.index("--")
        args = parser.parse_args(argv[:separator])
        launch(args, argv[separator + 1:])
        return 0
    except LauncherError as exc:
        print("Nix Compose guest launcher rejected: " + str(exc), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
