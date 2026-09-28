#!/usr/bin/env bash
set -euo pipefail

echo 'Direct site deployment is retired. Use the manual Kamal operator: task site:deploy -- status on main.' >&2
echo 'Activation requires a prepared production record and a verified host handover.' >&2
exit 64
