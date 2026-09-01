locals {
  ssh_public_key_path  = pathexpand(var.ssh_public_key_path)
  create_ssh_key       = trimspace(var.ssh_public_key_path) != "" && fileexists(local.ssh_public_key_path)
  ssh_private_key_path = endswith(local.ssh_public_key_path, ".pub") ? trimsuffix(local.ssh_public_key_path, ".pub") : local.ssh_public_key_path
  ssh_identity_arg     = local.create_ssh_key ? " -i ${local.ssh_private_key_path}" : ""
  domain               = trimspace(var.domain) != "" ? trimspace(var.domain) : "${replace(hcloud_primary_ip.leapview.ip_address, ".", "-")}.sslip.io"
  labels = {
    app = "leapview"
  }
}

resource "hcloud_primary_ip" "leapview" {
  name        = "${var.name}-ipv4"
  location    = var.location
  type        = "ipv4"
  auto_delete = false
  labels      = local.labels
}

resource "hcloud_ssh_key" "local" {
  count      = local.create_ssh_key ? 1 : 0
  name       = "${var.name}-local"
  public_key = file(local.ssh_public_key_path)
  labels     = local.labels
}

resource "hcloud_firewall" "leapview" {
  name   = "${var.name}-firewall"
  labels = local.labels

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = var.ssh_allowed_cidrs
  }

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "80"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  rule {
    direction  = "in"
    protocol   = "udp"
    port       = "443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
}

resource "hcloud_server" "leapview" {
  name                     = var.name
  server_type              = var.server_type
  image                    = var.image
  location                 = var.location
  ssh_keys                 = concat(var.ssh_key_ids, hcloud_ssh_key.local[*].id)
  backups                  = true
  shutdown_before_deletion = true
  firewall_ids = [
    hcloud_firewall.leapview.id,
  ]
  labels = local.labels

  public_net {
    ipv4_enabled = true
    ipv4         = hcloud_primary_ip.leapview.id
    ipv6_enabled = true
  }

  user_data = templatefile("${path.module}/../host/cloud-init.yaml.tftpl", {
    bootstrap_b64 = base64encode(file("${path.module}/../host/bootstrap-ubuntu.sh"))
    config_b64 = base64encode(jsonencode({
      schemaVersion = 1
      domain        = local.domain
      adminEmail    = var.admin_email
      environment   = "prod"
      image         = var.leapview_image
      https         = true
    }))
    image_b64  = base64encode("${var.leapview_image}\n")
  })
}
