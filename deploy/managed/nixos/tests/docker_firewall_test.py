"""Kernel regression test; run in fresh mount AND network namespaces as root.

sudo unshare --mount --net --fork python3 docker_firewall_test.py

This fixture supplies Docker-shaped forwarding/DNAT rules without starting a
daemon. The NixOS guest test additionally checks real Docker and service ordering.
All interfaces, namespace handles, processes and firewall mutations are isolated
from the operator's host. No registry or cloud access is needed.
"""

import json
import os
from pathlib import Path
import subprocess
import sys
import time


def run(*args, check=True):
    result = subprocess.run(args, capture_output=True, text=True, timeout=30)
    if check and result.returncode:
        raise RuntimeError(f"{args}: {result.stderr}")
    return result


def main():
    if os.geteuid() != 0 or any(
        os.readlink(f"/proc/self/ns/{kind}") == os.readlink(f"/proc/1/ns/{kind}")
        for kind in ("net", "mnt")
    ):
        raise SystemExit("Run as root with unshare --mount --net --fork")
    # Also refuse an occupied namespace, including invocations inside a container.
    if {link["ifname"] for link in json.loads(run("ip", "-j", "link", "show").stdout)} != {"lo"}:
        raise SystemExit("A fresh network namespace is required")

    policy = Path(__file__).resolve().parent.parent / "modules/docker-firewall.sh"
    run("mount", "--make-rprivate", "/")
    Path("/run/netns").mkdir(exist_ok=True)
    run("mount", "-t", "tmpfs", "tmpfs", "/run/netns")
    run("ip", "link", "set", "lo", "up")
    run("sysctl", "-qw", "net.ipv4.ip_forward=1")
    run("sysctl", "-qw", "net.ipv6.conf.all.forwarding=1")
    servers = []
    try:
        for name, interface, ipv4, ipv6 in (
            ("public", "eth-public", "198.18.0", "fd00:1"),
            ("private", "eth-private", "192.168.2", "fd00:2"),
            ("container", "docker0", "172.30.0", "fd00:3"),
        ):
            run("ip", "netns", "add", name)
            peer = f"{name}-peer"
            run("ip", "link", "add", interface, "type", "veth", "peer", "name", peer)
            run("ip", "link", "set", peer, "netns", name)
            run("ip", "addr", "add", f"{ipv4}.1/24", "dev", interface)
            run("ip", "-6", "addr", "add", f"{ipv6}::1/64", "dev", interface, "nodad")
            run("ip", "link", "set", interface, "up")
            run("ip", "netns", "exec", name, "ip", "addr", "add", f"{ipv4}.2/24", "dev", peer)
            run("ip", "netns", "exec", name, "ip", "-6", "addr", "add", f"{ipv6}::2/64", "dev", peer, "nodad")
            run("ip", "netns", "exec", name, "ip", "link", "set", peer, "up")
            run("ip", "netns", "exec", name, "ip", "link", "set", "lo", "up")
            run("ip", "netns", "exec", name, "ip", "route", "add", "default", "via", f"{ipv4}.1")
            run("ip", "netns", "exec", name, "ip", "-6", "route", "add", "default", "via", f"{ipv6}::1")

        for family, destination in (("iptables", "172.30.0.2"), ("ip6tables", "[fd00:3::2]")):
            run(family, "-N", "DOCKER-USER")
            run(family, "-A", "FORWARD", "-j", "DOCKER-USER")
            run(family, "-A", "FORWARD", "-o", "docker0", "-j", "ACCEPT")
            run(family, "-A", "FORWARD", "-i", "docker0", "-j", "ACCEPT")
            run(family, "-P", "FORWARD", "DROP")
            for port, target in ((80, 8080), (443, 8080), (8080, 8080), (9000, 80)):
                run(family, "-t", "nat", "-A", "PREROUTING", "-m", "addrtype", "--dst-type", "LOCAL", "-p", "tcp", "--dport", str(port), "-j", "DNAT", "--to-destination", f"{destination}:{target}")

        for namespace, port in (("container", 80), ("container", 8080), ("private", 8000)):
            servers.append(subprocess.Popen(
                ["ip", "netns", "exec", namespace, sys.executable, "-m", "http.server", str(port), "--bind", "::"],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            ))
        for address, port in (("172.30.0.2", 80), ("172.30.0.2", 8080), ("192.168.2.2", 8000)):
            deadline = time.monotonic() + 10
            while run("curl", "--noproxy", "*", "-s", "--max-time", "1", "-o", "/dev/null", f"http://{address}:{port}", check=False).returncode:
                if time.monotonic() >= deadline:
                    raise AssertionError(f"Fixture server {address}:{port} did not start")
                time.sleep(0.1)

        # Prove both direct routes are functional before the policy is installed,
        # so later denial cannot pass because of a missing route or listener.
        for namespace in ("public", "private"):
            for destination in ("172.30.0.2", "[fd00:3::2]"):
                for port in (80, 8080):
                    run("ip", "netns", "exec", namespace, "curl", "--noproxy", "*", "-s", "--max-time", "2", "-o", "/dev/null", f"http://{destination}:{port}")
        # Baseline connections used distinct client ports and have closed. New
        # requests below must be denied rather than inheriting established state.
        for iteration in range(2):
            run("bash", str(policy), "eth-public")
            for namespace, address in (
                ("public", "198.18.0.1"), ("public", "[fd00:1::1]"),
                ("private", "192.168.2.1"), ("private", "[fd00:2::1]"),
            ):
                for port in (80, 443, 8080, 9000):
                    allowed = run("ip", "netns", "exec", namespace, "curl", "--noproxy", "*", "-s", "--connect-timeout", "1", "--max-time", "2", "-o", "/dev/null", f"http://{address}:{port}", check=False).returncode == 0
                    expected = namespace == "public" and port in (80, 443)
                    if allowed != expected:
                        raise AssertionError(f"{namespace}:{address}:{port}: expected allowed={expected}, got {allowed}")
            for namespace in ("public", "private"):
                # Direct routes have no DNAT/original host-port authorization.
                for destination in ("172.30.0.2", "[fd00:3::2]"):
                    for port in (80, 8080):
                        result = run("ip", "netns", "exec", namespace, "curl", "--noproxy", "*", "-s", "--max-time", "1", "-o", "/dev/null", f"http://{destination}:{port}", check=False)
                        if result.returncode == 0:
                            raise AssertionError(f"Direct container ingress allowed: {namespace}:{destination}:{port}")
            for address in ("192.168.2.2", "[fd00:2::2]"):
                run("ip", "netns", "exec", "container", "curl", "--noproxy", "*", "-s", "--max-time", "2", "-o", "/dev/null", f"http://{address}:8000")
            print(f"Pass {iteration + 1}: IPv4/IPv6 proxy ingress, bypass/direct-route denial, private isolation and outbound responses", flush=True)
    finally:
        for server in servers:
            server.terminate()
            server.wait()
        for namespace in ("public", "private", "container"):
            run("ip", "netns", "delete", namespace, check=False)


if __name__ == "__main__":
    main()
