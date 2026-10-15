package managedstackkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// NodeChannel is the execution channel one node of a multi-node kit owns.
type NodeChannel struct {
	ChannelRef string
	SiteRef    string
	NodeRef    string
}

// BuildNodeInventory binds every node of a multi-node kit to the enrolled
// Operations process. Each node has exactly one channel, and the channels must
// cover exactly the nodes the ResolvedPlan declares. A plan that names a node
// the kit does not bind, or leaves one out, is refused: Apply would otherwise
// stop on `execution-channel-unbound` or, worse, run a node's local owners on
// the wrong host.
//
// The Inventory carries no backup or fabric binding. A plan that selects a
// managed Cloud backup target is refused until that capability is wired for
// the kit.
func BuildNodeInventory(resolvedPlan []byte, nodes []NodeChannel, executableSHA256 string) ([]byte, error) {
	if len(resolvedPlan) == 0 || len(resolvedPlan) > maxResolvedPlanBytes {
		return nil, errors.New("managed StackKits Inventory requires a bounded ResolvedPlan")
	}
	plan, err := decodeObject(resolvedPlan)
	if err != nil {
		return nil, fmt.Errorf("decode managed StackKits ResolvedPlan: %w", err)
	}
	if plan[apiVersionField] != "stackkit.resolved-plan/v1" {
		return nil, errors.New("managed StackKits Inventory requires stackkit.resolved-plan/v1")
	}
	if !validDigest(executableSHA256) {
		return nil, errors.New("managed StackKits Inventory requires an exact Operations executable digest")
	}
	requirement, _, _, err := exactBackupRequirement(plan)
	if err != nil {
		return nil, err
	}
	if requirement != nil {
		return nil, errors.New("managed multi-node StackKits Inventory does not bind a Cloud backup target")
	}
	if err := requireExactPlanNodes(plan, nodes); err != nil {
		return nil, err
	}
	channels := make(map[string]any, len(nodes))
	for _, node := range nodes {
		process := OperationsProcess{
			ChannelRef: node.ChannelRef, SiteRef: node.SiteRef, NodeRef: node.NodeRef,
			Executable: cloudOperationsExecutable, ExecutableSHA256: executableSHA256,
		}
		if err := validateOperationsProcess(process, node.SiteRef, node.NodeRef); err != nil {
			return nil, err
		}
		channels[node.ChannelRef] = map[string]any{
			apiVersionField: "stackkit.standard-execution-channel/v1", "kind": "StandardExecutionChannel",
			"channelRef": node.ChannelRef, "siteRef": node.SiteRef, "nodeRef": node.NodeRef, "operationClass": "standard",
			"operationsProcess": map[string]any{"executable": process.Executable, "executableSha256": process.ExecutableSHA256},
		}
	}
	encoded, err := json.Marshal(map[string]any{"schemaVersion": "stackkit.inventory/v1", "executionChannels": channels})
	if err != nil {
		return nil, fmt.Errorf("encode managed StackKits Inventory: %w", err)
	}
	return append(encoded, '\n'), nil
}

func requireExactPlanNodes(plan map[string]any, nodes []NodeChannel) error {
	rawNodes, ok := plan["nodes"].([]any)
	if !ok || len(rawNodes) == 0 || len(rawNodes) != len(nodes) {
		return errors.New("ResolvedPlan nodes do not match the nodes the managed kit binds")
	}
	planned := make([]string, 0, len(rawNodes))
	for _, raw := range rawNodes {
		node, ok := raw.(map[string]any)
		id, _ := node["id"].(string)
		site, _ := node["siteRef"].(string)
		if !ok || id == "" || site == "" {
			return errors.New("ResolvedPlan node has no exact id and Site")
		}
		planned = append(planned, site+"/"+id)
	}
	bound := make([]string, 0, len(nodes))
	for _, node := range nodes {
		bound = append(bound, node.SiteRef+"/"+node.NodeRef)
	}
	sort.Strings(planned)
	sort.Strings(bound)
	for index := range planned {
		if planned[index] != bound[index] {
			return fmt.Errorf("ResolvedPlan node %q is not the managed kit node %q", planned[index], bound[index])
		}
	}
	return nil
}
