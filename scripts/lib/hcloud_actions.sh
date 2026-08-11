#!/usr/bin/env bash

wait_hcloud_actions() {
  local response="$1"
  local action_ids action_id

  if ! action_ids="$(jq -er '
    [(.action.id?), (.actions[]?.id?)]
    | map(select(type == "number"))
    | unique
    | if length == 0 then error("Hetzner returned no action identities") else .[] end
  ' <<<"$response")"; then
    echo "Hetzner did not return any firewall action identities" >&2
    return 1
  fi

  while IFS= read -r action_id; do
    wait_hcloud_action "$action_id"
  done <<<"$action_ids"
}
