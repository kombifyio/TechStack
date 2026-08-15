// Package unifier implements the CUE-based Unifier Engine for kombifyTechstack.
//
// The Unifier is the core of kombifyTechstack's Spec-Driven Architecture.
// It validates user specs (kombination.yaml) against CUE schemas,
// resolves dependencies, enforces policies, and generates OpenTofu configurations.
//
// # Architecture Overview
//
//	kombination.yaml  →  Loader  →  Validator  →  Resolver  →  Generator  →  tfvars.json
//
// # Key Components
//
//   - Engine: Main Unifier interface implementation
//   - Loader: Parses YAML specs and converts to CUE values
//   - Validator: Checks specs against CUE schemas
//   - Generator: Produces OpenTofu tfvars.json output
//
// # CUE Schema Structure
//
//	pkg/unifier/schema/
//	├── base.cue       # Root KombinationSpec schema
//	├── node.cue       # Node definitions
//	├── service.cue    # Service definitions
//	└── network.cue    # Network configuration
//
// # Usage
//
//	engine := unifier.New()
//	result, err := engine.Validate(spec)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	if !result.Valid {
//	    // Handle validation errors
//	}
//	unified, err := engine.Unify(spec)
//	// Generate OpenTofu config from unified spec
package unifier
