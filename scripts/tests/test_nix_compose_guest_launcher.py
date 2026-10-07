import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

import nix_compose_guest_launcher as launcher


class FakeQemu:
    def __init__(self, command):
        self.command = command
        self.terminated = False
        self.killed = False
        self.boot_id = "boot-before-install"
        self.exit_code = None

    def poll(self):
        return self.exit_code if self.terminated else None

    def guest_reset(self):
        # QEMU's `-no-reboot` makes a guest reset exit QEMU; default QEMU resets in place.
        if "-no-reboot" in self.command:
            self.exit_code = 0
            self.terminated = True
        else:
            self.boot_id = "boot-after-install"

    def terminate(self):
        self.terminated = True
        self.exit_code = -15

    def kill(self):
        self.killed = True
        self.terminated = True
        self.exit_code = -9

    def wait(self, timeout=None):
        return self.exit_code


class GuestLauncherTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.image = self.root / "official-cloud.qcow2"
        self.image.write_bytes(b"immutable vendor cloud image bytes")
        self.tools = self.root / "tools"
        self.tools.mkdir()
        self.output_dir = self.root / "collector-output"
        self.launcher_receipt = self.root / "launcher-ready.json"
        self.lifecycle_receipt = self.root / "launcher-lifecycle.json"
        self.args = self._args()
        self.processes = []
        self.temp_root = self.root / "tmp"
        self.temp_root.mkdir()

    def tearDown(self):
        self.temporary.cleanup()

    def _args(self, *, architecture="amd64", firmware=None, expected_digest=None):
        digest = hashlib.sha256(self.image.read_bytes()).hexdigest()
        return launcher.argparse.Namespace(
            cloud_image=self.image,
            image_sha256=expected_digest or digest,
            guest_os="ubuntu2404",
            architecture=architecture,
            virtualization_mode="tcg",
            firmware=firmware,
            readiness_timeout=60,
            timeout=60,
            output_dir=self.output_dir,
            launcher_receipt=self.launcher_receipt,
            lifecycle_receipt=self.lifecycle_receipt,
        )

    def _make_tools(self, architecture):
        names = ["qemu-system-" + launcher.ARCHITECTURES[architecture], "qemu-img", "cloud-localds", "ssh-keygen"]
        tools = {}
        for name in names:
            path = self.tools / name
            path.write_bytes((name + " locked executable").encode())
            path.chmod(0o755)
            tools[name] = path
        return tools

    def _environment(self, *, architecture="amd64", collector_exit=0, ready=True):
        tools = self._make_tools(architecture)
        by_path = {str(path): name for name, path in tools.items()}
        self.processes.clear()
        userdata_seen = []
        collector_calls = []
        self.collector_guest_reset = None

        def fake_run(command, **kwargs):
            command = [str(arg) for arg in command]
            tool = by_path.get(command[0])
            if tool and tool.startswith("qemu-system-") and command[1:] == ["--version"]:
                return subprocess.CompletedProcess(command, 0, b"QEMU emulator version 10.0.0\n", b"")
            if tool == "qemu-img" and command[1:3] == ["info", "--output=json"]:
                return subprocess.CompletedProcess(command, 0, b'{"format":"qcow2","virtual-size":8589934592}\n', b"")
            if tool == "qemu-img" and command[1] == "create":
                Path(command[-1]).write_bytes(b"overlay")
                return subprocess.CompletedProcess(command, 0, b"", b"")
            if tool == "ssh-keygen":
                key_path = Path(command[command.index("-f") + 1])
                key_path.write_text("ephemeral private key\n")
                key_path.chmod(0o600)
                suffix = "client" if "client" in key_path.name else "host"
                (key_path.parent / (key_path.name + ".pub")).write_text(f"ssh-ed25519 AAAA{suffix} ephemeral-{suffix}\n")
                return subprocess.CompletedProcess(command, 0, b"", b"")
            if tool == "cloud-localds":
                output = Path(command[1])
                userdata_seen.append(Path(command[2]).read_bytes())
                output.write_bytes(b"mock cidata iso")
                return subprocess.CompletedProcess(command, 0, b"", b"")
            if command[0] == "ssh":
                if ready == "schema-error":
                    return subprocess.CompletedProcess(command, 2, b"", b"")
                if ready is False:
                    return subprocess.CompletedProcess(command, 255, b"", b"Connection timed out during banner exchange\n")
                return subprocess.CompletedProcess(command, 0, b"", b"")
            if "--install-mode" in command:
                collector_calls.append((command, kwargs))
                guest = self.processes[-1]
                boot_before = guest.boot_id
                guest.guest_reset()
                self.collector_guest_reset = (boot_before, guest.boot_id, guest.poll() is None)
                if guest.poll() is not None:
                    return subprocess.CompletedProcess(command, 88, b"QEMU exited on guest reboot", b"")
                if collector_exit == 0:
                    output_dir = Path(command[command.index("--output-dir") + 1])
                    output_dir.mkdir(parents=True)
                    (output_dir / "host-guest-receipt.json").write_text('{"result":"passed"}\n')
                return subprocess.CompletedProcess(command, collector_exit, b"", b"")
            raise AssertionError(f"unexpected subprocess command: {command}")

        def fake_popen(command, **kwargs):
            process = FakeQemu(command)
            self.processes.append(process)
            return process

        return tools, userdata_seen, collector_calls, fake_run, fake_popen

    def _run_launch(self, *, architecture="amd64", collector_exit=0, ready=True, firmware=None):
        self.args = self._args(architecture=architecture, firmware=firmware)
        tools, userdata_seen, collector_calls, fake_run, fake_popen = self._environment(
            architecture=architecture, collector_exit=collector_exit, ready=ready,
        )
        fake_time = [0.0]

        def monotonic():
            current = fake_time[0]
            fake_time[0] += 20
            return current

        with (
            patch.object(launcher.platform, "machine", return_value=launcher.ARCHITECTURES[architecture]),
            patch.object(launcher.shutil, "which", side_effect=lambda name: str(tools[name])),
            patch.object(launcher.subprocess, "run", side_effect=fake_run),
            patch.object(launcher.subprocess, "Popen", side_effect=fake_popen),
            patch.object(launcher.tempfile, "tempdir", str(self.temp_root)),
            patch.object(launcher.time, "sleep", return_value=None),
            patch.object(launcher.time, "monotonic", side_effect=monotonic),
            patch.object(launcher.secrets, "token_hex", return_value="a" * 64),
        ):
            receipt = None
            error = None
            try:
                receipt = launcher.launch(
                    self.args,
                    ["python3", "scripts/nix_compose_host_guest.py", "qualify-guest", "--install-mode", "bootstrap"],
                )
            except launcher.LauncherError as exc:
                error = exc
        return receipt, error, userdata_seen, collector_calls, tools

    def test_collector_guest_reset_reboots_in_place_then_launcher_cleans_after_return(self):
        receipt, error, user_data, collector_calls, tools = self._run_launch()
        self.assertIsNone(error)
        self.assertEqual(receipt["scope"], "nix-compose-guest-launcher")
        self.assertEqual(set(receipt["inputs"]), {
            "cloudImageSHA256", "userDataSHA256", "metaDataSHA256", "seedISOSHA256",
            "sshClientPublicKeySHA256", "sshHostPublicKeySHA256", "knownHostsSHA256", "firmwareSHA256",
        })
        self.assertIsNone(receipt["inputs"]["firmwareSHA256"])
        self.assertEqual(len(collector_calls), 1)
        command, options = collector_calls[0]
        self.assertEqual(options["timeout"], self.args.timeout)
        self.assertEqual(command[command.index("--host") + 1], "127.0.0.1")
        self.assertEqual(command[command.index("--guest-os") + 1], "ubuntu2404")
        self.assertEqual(command[command.index("--install-mode") + 1], "bootstrap")
        boot_before, boot_after, qemu_alive_after_reset = self.collector_guest_reset
        self.assertNotEqual(boot_before, boot_after)
        self.assertTrue(qemu_alive_after_reset)
        self.assertEqual(len(self.processes), 1)
        self.assertTrue(self.processes[0].terminated)
        self.assertIn("q35,accel=tcg", self.processes[0].command)
        self.assertIn("HostKeyAlgorithms=ssh-ed25519", " ".join(launcher._ssh_command(
            port=2222, identity=Path("client"), known_hosts=Path("known_hosts"), command="true",
        )))
        self.assertIn(b"ssh_keys", user_data[0])
        self.assertNotIn(b"docker.io", user_data[0])
        self.assertNotIn(b"provider", user_data[0].lower())
        self.assertFalse(any(self.temp_root.iterdir()))
        self.assertEqual((self.launcher_receipt.stat().st_mode & 0o777), 0o400)
        self.assertEqual((self.lifecycle_receipt.stat().st_mode & 0o777), 0o600)
        lifecycle = json.loads(self.lifecycle_receipt.read_text())
        self.assertEqual(lifecycle["result"], "passed")
        self.assertTrue(lifecycle["qemuTerminated"])
        self.assertTrue(lifecycle["tempDirectoryRemoved"])
        self.assertEqual(lifecycle["launcherReceiptSHA256"], launcher._digest(self.launcher_receipt.read_bytes()))
        self.assertEqual(lifecycle["guestReceiptSHA256"], launcher._file_digest(self.output_dir / "host-guest-receipt.json"))

    def test_collector_failure_still_kills_qemu_and_removes_temporary_guest(self):
        receipt, error, _, _, _ = self._run_launch(collector_exit=7)
        self.assertIsNone(receipt)
        self.assertRegex(str(error), r"collector failed \(7\)")
        lifecycle = json.loads(self.lifecycle_receipt.read_text())
        self.assertEqual(lifecycle["result"], "failed")
        self.assertEqual(lifecycle["collectorExitCode"], 7)
        self.assertIsNone(lifecycle["guestReceiptSHA256"])
        self.assertTrue(lifecycle["qemuTerminated"])
        self.assertTrue(lifecycle["tempDirectoryRemoved"])
        self.assertTrue(self.processes[0].terminated)
        self.assertFalse(any(self.temp_root.iterdir()))

    def test_cloud_init_timeout_cleans_up_without_starting_collector(self):
        receipt, error, _, collector_calls, _ = self._run_launch(ready=False)
        self.assertIsNone(receipt)
        self.assertRegex(str(error), "readiness timed out")
        self.assertRegex(str(error), r"SSH readiness command exited 255")
        self.assertEqual(collector_calls, [])
        lifecycle = json.loads(self.lifecycle_receipt.read_text())
        self.assertEqual(lifecycle["failurePhase"], "readiness")
        self.assertIsNone(lifecycle["launcherReceiptSHA256"])
        self.assertTrue(lifecycle["qemuTerminated"])
        self.assertTrue(lifecycle["tempDirectoryRemoved"])

    def test_readiness_error_reports_guest_command_exit_code(self):
        receipt, error, _, collector_calls, _ = self._run_launch(ready="schema-error")
        self.assertIsNone(receipt)
        self.assertRegex(str(error), r"SSH readiness command exited 2")
        self.assertEqual(collector_calls, [])

    def test_cloud_config_uses_only_ed25519_host_key_generation(self):
        config = launcher._cloud_config(
            client_public_key=b"ssh-ed25519 AAAAclient client",
            host_private_key=b"-----BEGIN OPENSSH PRIVATE KEY-----\nsecret\n-----END OPENSSH PRIVATE KEY-----\n",
            host_public_key=b"ssh-ed25519 AAAAhost host",
            manifest_bytes=b"{}\n",
        ).split(b"\n", 1)[1]
        self.assertEqual(json.loads(config)["ssh_genkeytypes"], ["ed25519"])

    def test_wrong_image_digest_fails_before_tools_or_qemu(self):
        self.args = self._args(expected_digest="0" * 64)
        with patch.object(launcher.platform, "machine", return_value="x86_64"):
            with self.assertRaisesRegex(launcher.LauncherError, "bytes differ"):
                launcher.launch(self.args, ["python3", "collector", "--install-mode", "bootstrap"])
        lifecycle = json.loads(self.lifecycle_receipt.read_text())
        self.assertEqual(lifecycle["result"], "failed")
        self.assertEqual(lifecycle["failurePhase"], "preflight")
        self.assertTrue(lifecycle["tempDirectoryRemoved"])
        self.assertFalse(self.launcher_receipt.exists())

    def test_selected_kvm_fails_closed_without_api_support(self):
        self.args = self._args()
        self.args.virtualization_mode = "kvm"
        with (
            patch.object(launcher.platform, "machine", return_value="x86_64"),
            patch.object(launcher, "_kvm_api_version", return_value=None),
        ):
            with self.assertRaisesRegex(launcher.LauncherError, "KVM selected"):
                launcher.launch(self.args, ["python3", "collector", "--install-mode", "bootstrap"])
        lifecycle = json.loads(self.lifecycle_receipt.read_text())
        self.assertEqual(lifecycle["failurePhase"], "preflight")
        self.assertTrue(lifecycle["qemuTerminated"])
        self.assertTrue(lifecycle["tempDirectoryRemoved"])
        self.assertEqual(list(self.temp_root.iterdir()), [])

    def test_qcow2_backing_chain_and_external_data_are_rejected(self):
        for info in (
            {"format": "qcow2", "backing-filename": "untrusted-base.qcow2"},
            {"format": "qcow2", "format-specific": {"data": {"data-file": "outside.raw"}}},
        ):
            with self.subTest(info=info), patch.object(
                launcher, "_run", return_value=json.dumps(info).encode(),
            ):
                with self.assertRaisesRegex(launcher.LauncherError, "backing chain or external"):
                    launcher._validate_qcow2(Path("qemu-img"), self.image)

    def test_native_arm_launch_binds_locked_firmware_and_passes_bios(self):
        firmware = self.root / "AAVMF_CODE.fd"
        firmware.write_bytes(b"locked ARM firmware")
        receipt, error, _, _, _ = self._run_launch(architecture="arm64", firmware=firmware)
        self.assertIsNone(error)
        self.assertEqual(receipt["inputs"]["firmwareSHA256"], launcher._file_digest(firmware))
        command = self.processes[0].command
        self.assertEqual(command[command.index("-machine") + 1], "virt,accel=tcg")
        self.assertEqual(command[command.index("-bios") + 1], str(firmware))
        self.assertTrue(self.processes[0].terminated)


if __name__ == "__main__":
    unittest.main()
