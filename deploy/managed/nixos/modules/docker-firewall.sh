#!/usr/bin/env bash
# Invoked by the host firewall before Docker starts. Keep this policy installed
# when that firewall stops: disabling INPUT filtering must not expose containers.
set -euo pipefail

public_interface=${1:?public interface is required}
if [[ ! "$public_interface" =~ ^[a-zA-Z0-9_.:-]{1,15}$ ]]; then
  echo "Invalid public interface name" >&2
  exit 1
fi

for family in iptables ip6tables; do
  # Docker also creates this chain; creating it first avoids an unfiltered boot
  # interval. A failed create is acceptable only if the chain already exists.
  "$family" -w -N DOCKER-USER 2>/dev/null || "$family" -w -S DOCKER-USER >/dev/null

  # Replace only our chain in one netfilter transaction, preserving Docker's
  # rules and the previous policy if validation/commit fails.
  "$family-restore" -w --noflush <<RULES
*filter
:LEAPVIEW-DOCKER - [0:0]
-F LEAPVIEW-DOCKER
-A LEAPVIEW-DOCKER -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
-A LEAPVIEW-DOCKER -i docker+ -j RETURN
-A LEAPVIEW-DOCKER -i br+ -j RETURN
-A LEAPVIEW-DOCKER -i $public_interface -p tcp -m conntrack --ctstate DNAT --ctdir ORIGINAL --ctorigdstport 80 -j RETURN
-A LEAPVIEW-DOCKER -i $public_interface -p tcp -m conntrack --ctstate DNAT --ctdir ORIGINAL --ctorigdstport 443 -j RETURN
-A LEAPVIEW-DOCKER -j DROP
COMMIT
RULES

  # RETURN delegates allowed traffic to Docker's own publication/isolation
  # rules. Match the bridge output rather than enumerating ingress interfaces,
  # so private, Tailscale and newly added interfaces all default to denial.
  for bridge in docker+ br+; do
    "$family" -w -C DOCKER-USER -o "$bridge" -j LEAPVIEW-DOCKER 2>/dev/null ||
      "$family" -w -I DOCKER-USER 1 -o "$bridge" -j LEAPVIEW-DOCKER
  done
  "$family" -w -C FORWARD -j DOCKER-USER 2>/dev/null ||
    "$family" -w -I FORWARD 1 -j DOCKER-USER
done
