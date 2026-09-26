locals {
  canonical_hostname = "leapview.dev"
  labels = {
    app         = "leapview-site"
    environment = "production"
    managed-by  = "terraform"
  }
}

resource "hcloud_primary_ip" "site" {
  name              = "${var.name}-ipv4"
  location          = var.location
  type              = "ipv4"
  auto_delete       = false
  delete_protection = true
  labels            = local.labels

  lifecycle {
    prevent_destroy = true
  }
}

resource "hcloud_ssh_key" "operator" {
  name       = "${var.name}-operator"
  public_key = trimspace(var.operator_ssh_public_key)
  labels     = local.labels
}

resource "hcloud_firewall" "site" {
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

  lifecycle {
    prevent_destroy = true
  }
}

resource "hcloud_server" "site" {
  name                     = var.name
  server_type              = var.server_type
  image                    = var.server_image
  location                 = var.location
  ssh_keys                 = [hcloud_ssh_key.operator.id]
  backups                  = false
  delete_protection        = true
  rebuild_protection       = true
  shutdown_before_deletion = true
  firewall_ids             = [hcloud_firewall.site.id]
  labels                   = local.labels

  public_net {
    ipv4_enabled = true
    ipv4         = hcloud_primary_ip.site.id
    ipv6_enabled = false
  }

  user_data = templatefile("${path.module}/cloud-init.yaml.tftpl", {
    compose_b64   = base64encode(file("${path.module}/files/compose.yaml"))
    caddyfile_b64 = base64encode(file("${path.module}/files/Caddyfile"))
    deployment_env_b64 = base64encode(templatefile("${path.module}/files/deployment.env.tftpl", {
      leapview_site_image = var.bootstrap_site_image
      caddy_image         = var.caddy_image
    }))
    provision_b64 = base64encode(file("${path.module}/files/provision.sh"))
    deploy_b64    = base64encode(file("${path.module}/files/deploy.sh"))
    retention_b64 = base64encode(file("${path.module}/files/site_image_retention.py"))
    reconcile_b64 = base64encode(file("${path.module}/files/reconcile.sh"))
    reconcile_service_b64 = base64encode(
      file("${path.module}/files/leapview-site-reconcile.service")
    )
    reconcile_timer_b64 = base64encode(
      file("${path.module}/files/leapview-site-reconcile.timer")
    )
  })

  lifecycle {
    prevent_destroy = true

    # Cloud-init is creation-only. Routine releases update deployment.env over
    # the bounded deployment channel owned by LEA-143 and must never replace
    # the server or its reserved address.
    ignore_changes = [user_data]
  }
}
