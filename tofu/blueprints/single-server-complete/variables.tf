# kombifyTechstack Single Server Complete Blueprint - Variables
#
# This file defines all input variables for the single-server-complete blueprint.

variable "stack_name" {
  description = "Name of this kombifyTechstack deployment"
  type        = string
  default     = "techstack"
}

variable "hcloud_token" {
  description = "Hetzner Cloud API token"
  type        = string
  sensitive   = true
}

variable "server_type" {
  description = "Hetzner server type (e.g., cx22, cpx21)"
  type        = string
  default     = "cx22"
}

variable "location" {
  description = "Datacenter location"
  type        = string
  default     = "nbg1"
}

variable "image" {
  description = "OS image to use"
  type        = string
  default     = "ubuntu-24.04"
}

variable "domain" {
  description = "Base domain for services"
  type        = string
  default     = "kombi.local"
}

variable "admin_username" {
  description = "Default admin username"
  type        = string
  default     = "kombi"
}

variable "admin_ssh_key" {
  description = "SSH public key for admin user"
  type        = string

  validation {
    condition     = can(regex("^(ssh-rsa|ssh-ed25519|ecdsa-sha2-nistp)", var.admin_ssh_key))
    error_message = "The admin_ssh_key must be a valid SSH public key (ssh-rsa, ssh-ed25519, or ecdsa)."
  }
}

variable "admin_password_hash" {
  description = "Optional password hash for admin user (mkpasswd --method=SHA-512)"
  type        = string
  default     = ""
  sensitive   = true
}

variable "enable_headscale" {
  description = "Enable Headscale VPN"
  type        = bool
  default     = true
}

variable "enable_pocketbase" {
  description = "Enable Pocketbase backend"
  type        = bool
  default     = true
}

variable "enable_dokploy" {
  description = "Enable Dokploy PaaS"
  type        = bool
  default     = true
}

variable "headscale_domain" {
  description = "Public domain for Headscale server"
  type        = string
  default     = ""
}

variable "labels" {
  description = "Additional labels for resources"
  type        = map(string)
  default     = {}
}
