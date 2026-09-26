#!/usr/bin/env bash
set -euo pipefail

echo 'Direct site deployment is retired. Use the serialized Deploy public site workflow on main.' >&2
echo 'Activation requires LEAPVIEW_SITE_DEPLOYMENT_MODE=kamal and a verified host handover.' >&2
exit 64
