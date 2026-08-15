# Backend configuration for state management
# This file configures the OpenTofu/Terraform backend for each stack

generate_hcl "_terramate_generated_backend.tf" {
  content {
    terraform {
      backend "local" {
        path = "${terramate.stack.path.fs.absolute}/terraform.tfstate"
      }
    }
  }
}

# Alternative: Remote backend configuration (uncomment to use)
# generate_hcl "_terramate_generated_backend.tf" {
#   content {
#     terraform {
#       backend "s3" {
#         bucket         = "{{.BackendBucket}}"
#         key            = "${terramate.stack.path.relative}/terraform.tfstate"
#         region         = "{{.BackendRegion}}"
#         encrypt        = true
#         dynamodb_table = "{{.BackendLockTable}}"
#       }
#     }
#   }
# }
