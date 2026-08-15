# Advanced Mode Templates

These templates are compatibility fixtures for the embedded `base-kit`.
Canonical StackKit template authoring lives in `kombify-StackKits`.

This directory contains Go template files for generating Terramate stack structures
in kombify Techstack's Advanced Mode deployment.

## Directory Structure

```
templates/advanced/
├── terramate.tm.hcl.tpl    # Root Terramate configuration
├── globals.tm.hcl.tpl      # Shared global variables
├── backend.tm.hcl.tpl      # State backend configuration
└── stacks/
    ├── network/
    │   └── stack.tm.hcl.tpl    # Network stack (order: 1, no deps)
    ├── services/
    │   └── stack.tm.hcl.tpl    # Services stack (order: 2, after: network)
    └── applications/
        └── stack.tm.hcl.tpl    # Applications stack (order: 3, after: services)
```

## Template Variables

All templates use Go's `text/template` syntax with the following data structures:

### Root Templates

**terramate.tm.hcl.tpl**: No variables needed - static configuration.

**globals.tm.hcl.tpl**:

- `{{.StackName}}` - Name of the stack
- `{{.Environment}}` - Deployment environment (production, staging, etc.)
- `{{.Network.VPNEnabled}}` - Boolean for VPN status
- `{{.Network.Subnet}}` - Network subnet CIDR
- `{{.Network.Domain}}` - Internal domain name
- `{{range .Nodes}}...{{end}}` - Iterate over nodes
  - `{{.Name}}` - Node name
  - `{{.IP}}` - Node IP address
  - `{{.Role}}` - Node role (main, worker)
  - `{{.Provider}}` - Infrastructure provider

**backend.tm.hcl.tpl**:

- `{{.BackendBucket}}` - S3 bucket for remote state (optional)
- `{{.BackendRegion}}` - AWS region (optional)
- `{{.BackendLockTable}}` - DynamoDB table for locking (optional)

### Stack Templates

**stacks/\*/stack.tm.hcl.tpl**:

- `{{.StackID}}` - Unique stack identifier (format: `<name>-<stack>`)

## Usage

The `AdvancedGenerator` in `pkg/tofu/generator_advanced.go` uses these templates
to generate a complete Terramate project structure from a `UnifiedSpec`.

```go
gen, err := NewAdvancedGenerator(stackKitConfig, workDir)
if err != nil {
    return err
}

err = gen.Generate(unifiedSpec)
```

## Generated Output

After generation, the work directory will contain:

```
workdir/
├── terramate.tm.hcl      # Terramate root config
├── globals.tm.hcl        # Shared variables
├── backend.tm.hcl        # State backend
└── stacks/
    ├── network/
    │   ├── stack.tm.hcl
    │   ├── main.tf
    │   ├── variables.tf
    │   └── outputs.tf
    ├── services/
    │   └── ...
    └── applications/
        └── ...
```

## Stack Ordering

Stacks are deployed in order based on their `after` dependencies:

1. **network** - No dependencies, runs first
2. **services** - Depends on network
3. **applications** - Depends on services

This ordering is enforced by Terramate's `after` clause and ensures
infrastructure is provisioned in the correct sequence.
