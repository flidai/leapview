mock_provider "hcloud" {}

variables {
  hcloud_token        = "test-token"
  admin_email         = "admin@example.com"
  target_id           = "deployment-target-1"
  leapview_image      = "ghcr.io/flidai/leapview@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  ssh_allowed_cidrs   = ["203.0.113.10/32"]
  ssh_public_key_path = ""
  ssh_key_ids         = ["existing-key"]
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

  assert {
    condition     = local.bootstrap_config.targetId == var.target_id
    error_message = "host install bootstrap must receive the authoritative deployment target ID"
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

run "reject_revision019_without_bootstrap_controller" {
  command = plan

  variables {
    leapview_image = "ghcr.io/flidai/leapview@sha256:4a4455ff0048704acf0df1a9308a39a09b4c786f801fe7f3a383ada089d21368"
  }

  expect_failures = [hcloud_server.leapview]
}

run "revision019_receives_pinned_controller_and_canonical_postgres_init" {
  command = plan

  variables {
    leapview_image             = "ghcr.io/flidai/leapview@sha256:4a4455ff0048704acf0df1a9308a39a09b4c786f801fe7f3a383ada089d21368"
    bootstrap_controller_image = "ghcr.io/flidai/leapview@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    domain                     = "dash.example.com"
  }

  assert {
    condition     = strcontains(local.bootstrap_cloud_init, base64encode("${var.bootstrap_controller_image}\n"))
    error_message = "the exact controller image must reach the predecessor bootstrap"
  }

  assert {
    condition     = strcontains(local.bootstrap_cloud_init, base64encode(file("${path.module}/../postgres/init.sh")))
    error_message = "the canonical PostgreSQL init script must reach the predecessor bootstrap"
  }
}
