variable "hcloud_token" {
  description = "Hetzner Cloud API token. Prefer HCLOUD_TOKEN or TF_VAR_hcloud_token from your shell instead of a tfvars file."
  type        = string
  sensitive   = true
  nullable    = true
  default     = null
}

variable "name" {
  description = "Name prefix for Hetzner resources."
  type        = string
  default     = "leapview"
}

variable "server_type" {
  description = "Hetzner server type. cpx22 is the supported baseline for a small LeapView instance."
  type        = string
  default     = "cpx22"
}

variable "location" {
  description = "Hetzner location for the server and reserved primary IPv4."
  type        = string
  default     = "fsn1"
}

variable "image" {
  description = "Base operating-system image."
  type        = string
  default     = "ubuntu-24.04"

  validation {
    condition     = var.image == "ubuntu-24.04"
    error_message = "image must be ubuntu-24.04; the automated host contract supports Ubuntu 24.04 LTS only."
  }
}

variable "ssh_allowed_cidrs" {
  description = "Explicit CIDR ranges allowed to reach SSH. Use the operator's public address with a /32 suffix."
  type        = list(string)

  validation {
    condition = length(var.ssh_allowed_cidrs) > 0 && alltrue([
      for cidr in var.ssh_allowed_cidrs :
      can(cidrhost(cidr, 0)) && cidr != "0.0.0.0/0" && cidr != "::/0"
    ])
    error_message = "ssh_allowed_cidrs must contain valid, restricted CIDRs; world-open SSH is not supported."
  }
}

variable "ssh_key_ids" {
  description = "Existing Hetzner SSH key names or IDs to attach to the server."
  type        = list(string)
  default     = []
}

variable "ssh_public_key_path" {
  description = "Local SSH public key to upload as a Hetzner SSH key. Set to empty to disable."
  type        = string
  default     = "~/.ssh/id_ed25519.pub"
}

variable "domain" {
  description = "Hostname for HTTPS. Leave empty to use a reserved-IP sslip.io hostname."
  type        = string
  default     = ""
}

variable "admin_email" {
  description = "Initial platform admin and local-login email."
  type        = string

  validation {
    condition     = can(regex("^[^@[:space:]]+@[^@[:space:]]+$", var.admin_email))
    error_message = "admin_email must be a valid email address."
  }
}

variable "leapview_image" {
  description = "Public LeapView OCI image pinned to an immutable sha256 digest."
  type        = string

  validation {
    condition     = can(regex("^[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}$", var.leapview_image))
    error_message = "leapview_image must be an immutable OCI reference ending in @sha256:<64 lowercase hex characters>."
  }
}

variable "release_transition_policy_path" {
  description = "Path to the candidate archive's post-admission release-transition-policy.json bound to leapview_image."
  type        = string

  validation {
    condition     = can(file(var.release_transition_policy_path))
    error_message = "release_transition_policy_path must name a readable candidate-bound policy file."
  }
}
