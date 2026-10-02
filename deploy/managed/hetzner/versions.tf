terraform {
  required_version = ">= 1.10.0, < 2.0.0"
  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "~> 1.58"
    }
  }
}

# Read HCLOUD_TOKEN from the operator/CI environment. Never put tokens in tfvars.
provider "hcloud" {}
