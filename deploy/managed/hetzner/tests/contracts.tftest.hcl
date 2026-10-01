mock_provider "hcloud" {
  mock_resource "hcloud_network" { defaults = { id = "100" } }
  mock_resource "hcloud_firewall" { defaults = { id = "200" } }
  mock_resource "hcloud_server" { defaults = { id = "300", ipv4_address = "203.0.113.20" } }
}

variables {
  customer       = "example"
  ssh_key_ids    = ["existing-operator-key"]
  operator_cidrs = ["203.0.113.10/32"]
}

run "isolated_two_host_plan" {
  command = plan
  assert {
    condition     = length(hcloud_server.host) == 2 && hcloud_server.host["app"].name != hcloud_server.host["database"].name
    error_message = "Each customer needs separate application and database hosts."
  }
  assert {
    condition     = alltrue([for host in hcloud_server.host : host.delete_protection && host.rebuild_protection])
    error_message = "Persistent hosts must be protected from accidental API deletion/rebuild."
  }
  assert {
    condition     = alltrue([for rule in hcloud_firewall.database.rule : rule.port != "5432" && rule.port != "80" && rule.port != "443"])
    error_message = "Database ports must not be opened on the public interface."
  }
  assert {
    condition     = hcloud_server_network.host["app"].ip == "10.42.0.10" && hcloud_server_network.host["database"].ip == "10.42.0.20"
    error_message = "Stable private addresses must match the host inventory contract."
  }
}

run "reject_public_ssh" {
  command = plan
  variables { operator_cidrs = ["0.0.0.0/0"] }
  expect_failures = [var.operator_cidrs]
}

run "require_operator_key" {
  command = plan
  variables { ssh_key_ids = [] }
  expect_failures = [var.ssh_key_ids]
}

run "reject_non_european_location" {
  command = plan
  variables { location = "ash" }
  expect_failures = [var.location]
}

run "reject_public_network_range" {
  command = plan
  variables { private_cidr = "203.0.113.0/24" }
  expect_failures = [var.private_cidr]
}
