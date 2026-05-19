variable "region" {
  type    = string
  default = "ap-south-1" # Mumbai — closest to most IICPC participants
}

variable "name_prefix" {
  type    = string
  default = "iicpc"
}

variable "environment" {
  type    = string
  default = "demo"
}

variable "instance_type" {
  description = "EC2 size. t3.large = 2 vCPU / 8GB RAM, enough for ~500 bots × 50k orders/sec."
  type        = string
  default     = "t3.large"
}

variable "root_disk_gb" {
  type    = number
  default = 100
}

variable "ssh_key_name" {
  description = "Name of an existing EC2 key pair (or empty to disable SSH, rely on SSM)."
  type        = string
  default     = ""
}

variable "admin_cidrs" {
  description = "CIDR ranges allowed to SSH. Defaults to none — use SSM instead."
  type        = list(string)
  default     = []
}

variable "repo_url" {
  description = "Git repo to clone on first boot. Switch to private SSH URL + deploy key for prod."
  type        = string
  default     = "https://github.com/your-team/iicpc-platform.git"
}
