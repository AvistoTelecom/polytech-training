variable "resource_group_location" {
  type        = string
  default     = "westeurope"
  description = "Location of the resource group."
}

variable "registry_url" {
  type        = string
  default     = ""
  description = "Registry URL."
}

variable "web_app_vote_docker_image_name" {
  type        = string
  default     = ""
  description = "Docker image of vote."
}
