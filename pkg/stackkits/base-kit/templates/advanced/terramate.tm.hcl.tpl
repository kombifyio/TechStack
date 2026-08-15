terramate {
  config {
    git {
      default_branch = "main"
    }
    run {
      env {
        TF_PLUGIN_CACHE_DIR = "${terramate.root.path.fs.absolute}/.terraform-cache"
      }
    }
  }
}
