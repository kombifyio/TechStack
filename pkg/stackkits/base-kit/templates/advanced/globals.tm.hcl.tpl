globals {
  stack_name  = "{{.StackName}}"
  environment = "{{.Environment}}"

  # Network configuration
  network = {
    vpn_enabled = {{.Network.VPNEnabled}}
    subnet      = "{{.Network.Subnet}}"
    domain      = "{{.Network.Domain}}"
  }

  # Node definitions
  nodes = {
{{- range .Nodes}}
    {{.Name}} = {
      ip       = "{{.IP}}"
      role     = "{{.Role}}"
      provider = "{{.Provider}}"
    }
{{- end}}
  }

  # Service definitions
  services = {
{{- range .Services}}
    {{.Name}} = {
      type   = "{{.Type}}"
      node   = "{{.Node}}"
      port   = {{.Port}}
      image  = "{{.Image}}"
    }
{{- end}}
  }

  # Common labels for all resources
  common_labels = {
    managed_by  = "techstack"
    stack       = global.stack_name
    environment = global.environment
  }
}
