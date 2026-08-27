mock_provider "hcloud" {}

variables {
  hcloud_token                   = "test-token"
  admin_email                    = "admin@example.com"
  leapview_image                 = "ghcr.io/flidai/leapview@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  release_transition_policy_path = "../../internal/platform/compatibility/release-transition-policy.json"
  ssh_allowed_cidrs              = ["203.0.113.10/32"]
  ssh_public_key_path            = ""
  ssh_key_ids                    = ["existing-key"]
}

run "secure_single_node_plan" {
  command = plan

  assert {
    condition     = hcloud_server.leapview.backups
    error_message = "daily Hetzner backups must be enabled"
  }

  assert {
    condition     = hcloud_server.leapview.shutdown_before_deletion
    error_message = "the server must shut down cleanly before deletion"
  }

  assert {
    condition     = length(hcloud_firewall.leapview.rule) == 4
    error_message = "the firewall must expose restricted SSH plus HTTP, HTTPS, and HTTP/3"
  }
}

run "reject_world_open_ssh" {
  command = plan

  variables {
    ssh_allowed_cidrs = ["0.0.0.0/0"]
  }

  expect_failures = [var.ssh_allowed_cidrs]
}

run "reject_mutable_application_image" {
  command = plan

  variables {
    leapview_image = "ghcr.io/flidai/leapview:latest"
  }

  expect_failures = [var.leapview_image]
}

run "reject_unsupported_base_image" {
  command = plan

  variables {
    image = "ubuntu-22.04"
  }

  expect_failures = [var.image]
}
