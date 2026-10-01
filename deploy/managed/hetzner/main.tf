locals {
  labels = { application = "leapview", customer = var.customer, profile = "managed-nixos" }
  hosts = {
    app      = { server_type = var.server_types.app, private_ip = cidrhost(var.private_cidr, 10) }
    database = { server_type = var.server_types.database, private_ip = cidrhost(var.private_cidr, 20) }
  }
}

resource "hcloud_network" "customer" {
  name     = "leapview-${var.customer}"
  ip_range = var.private_cidr
  labels   = local.labels
  lifecycle { prevent_destroy = true }
}

resource "hcloud_network_subnet" "customer" {
  network_id   = hcloud_network.customer.id
  type         = "cloud"
  network_zone = "eu-central"
  ip_range     = var.private_cidr
}

resource "hcloud_firewall" "app" {
  name   = "leapview-${var.customer}-app"
  labels = local.labels
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = var.operator_cidrs
  }
  dynamic "rule" {
    for_each = ["80", "443"]
    content {
      direction  = "in"
      protocol   = "tcp"
      port       = rule.value
      source_ips = ["0.0.0.0/0", "::/0"]
    }
  }
}

resource "hcloud_firewall" "database" {
  name   = "leapview-${var.customer}-database"
  labels = local.labels
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = var.operator_cidrs
  }
}

resource "hcloud_server" "host" {
  for_each                 = local.hosts
  name                     = "leapview-${var.customer}-${each.key}"
  server_type              = each.value.server_type
  location                 = var.location
  image                    = "ubuntu-24.04" # Temporary SSH installer, replaced by nixos-anywhere.
  ssh_keys                 = var.ssh_key_ids
  labels                   = merge(local.labels, { role = each.key })
  firewall_ids             = [each.key == "app" ? hcloud_firewall.app.id : hcloud_firewall.database.id]
  delete_protection        = true
  rebuild_protection       = true
  shutdown_before_deletion = true
  public_net {
    ipv4_enabled = true
    ipv6_enabled = false
  }
  lifecycle {
    prevent_destroy = true
    # NixOS owns the installed OS and authorized keys after initial installation.
    ignore_changes = [image, ssh_keys]
  }
}

resource "hcloud_server_network" "host" {
  for_each  = local.hosts
  server_id = hcloud_server.host[each.key].id
  subnet_id = hcloud_network_subnet.customer.id
  ip        = each.value.private_ip
}
