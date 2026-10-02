variable "customer" {
  description = "Non-secret customer identifier used in names and labels."
  type        = string
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,30}$", var.customer))
    error_message = "customer must be 2-31 lowercase letters, digits or hyphens, starting with a letter."
  }
}

variable "location" {
  type        = string
  default     = "fsn1"
  description = "European location; both hosts share a location for private-network latency."
  validation {
    condition     = contains(["fsn1", "nbg1", "hel1"], var.location)
    error_message = "The v1 managed profile requires fsn1, nbg1 or hel1."
  }
}

variable "server_types" {
  type        = object({ app = string, database = string })
  default     = { app = "cpx32", database = "cpx32" }
  description = "Example x86_64 sizes, independently selected after workload measurement."
}

variable "ssh_key_ids" {
  type        = list(string)
  description = "Existing Hetzner SSH key names/IDs for initial installation and rescue access."
  validation {
    condition     = length(var.ssh_key_ids) > 0 && alltrue([for key in var.ssh_key_ids : trimspace(key) != ""])
    error_message = "At least one existing operator SSH key is required."
  }
}

variable "operator_cidrs" {
  type        = list(string)
  description = "Restricted public CIDRs for initial SSH and emergency access; normal administration uses Tailscale."
  validation {
    condition = length(var.operator_cidrs) > 0 && alltrue([
      for cidr in var.operator_cidrs : can(cidrhost(cidr, 0)) && try(tonumber(split("/", cidr)[1]) > 0, false)
    ])
    error_message = "Provide valid restricted operator CIDRs; default routes are forbidden."
  }
}

variable "private_cidr" {
  type        = string
  default     = "10.42.0.0/24"
  description = "Customer private IPv4 /24. App uses host 10 and PostgreSQL host 20."
  validation {
    condition     = can(cidrnetmask(var.private_cidr)) && can(regex("/24$", var.private_cidr)) && can(regex("^(10\\.|192\\.168\\.|172\\.(1[6-9]|2[0-9]|3[01])\\.)", var.private_cidr))
    error_message = "private_cidr must be an RFC1918 IPv4 /24."
  }
}
