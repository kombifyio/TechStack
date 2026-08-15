// Package hybrid_cloud provides a StackKit for hybrid homelab + cloud deployments.
//
// Goal (minimal, for now): make the resolved "hybrid-cloud" StackKit loadable and
// schema-validatable by the Unifier pipeline.

package hybrid_cloud

import "github.com/kombihq/stackkits/base"

// #HybridCloudKit extends the base StackKit with constraints for hybrid setups.
#HybridCloudKit: base.#BaseStackKit & {
    metadata: {
        name:        "hybrid-cloud"
        displayName: "Hybrid Cloud"
        version:     "1.0.0"
        description: "Hybrid deployment: local main node + cloud workers"
        author:      "kombifyTechstack Team"
        license:     "MIT"
        tags:        ["hybrid", "cloud", "multi-node"]

        minkombifyTechstackVersion: "1.0.0"
    }

    // Keep defaults; require multi-node.
    _nodeConstraints: {
        minNodes: 2
        maxNodes: 0 // 0 means "no upper bound" (convention in code/tests)
        types:    ["main", "worker"]
    }
}
