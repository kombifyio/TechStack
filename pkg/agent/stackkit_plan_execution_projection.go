package agent

import (
	"encoding/json"
	"fmt"
)

// appendPlanExecutionProjection keeps host credentials and custody material
// out of the read receipt. The bytes come only from Guard's confined canonical
// artifact after the pinned PLAN verified its generation.
func appendPlanExecutionProjection(plan, projection map[string]json.RawMessage) error {
	var nodes []map[string]json.RawMessage
	if json.Unmarshal(plan["nodes"], &nodes) != nil {
		return fmt.Errorf("inspected plan nodes are invalid")
	}
	nodeProjection := make([]map[string]json.RawMessage, 0, len(nodes))
	for _, node := range nodes {
		nodeProjection = append(nodeProjection, map[string]json.RawMessage{"id": node["id"], "siteRef": node["siteRef"]})
	}
	var bindings map[string]map[string]json.RawMessage
	if raw, exists := plan["externalHostBindings"]; exists && json.Unmarshal(raw, &bindings) != nil {
		return fmt.Errorf("inspected plan external execution bindings are invalid")
	}
	publicBindings := make(map[string]map[string]json.RawMessage, len(bindings))
	for nodeRef, binding := range bindings {
		publicBindings[nodeRef] = map[string]json.RawMessage{
			"nodeRef": binding["nodeRef"], "executionChannelRef": binding["executionChannelRef"],
		}
	}
	var err error
	projection["nodes"], err = json.Marshal(nodeProjection)
	if err != nil {
		return err
	}
	projection["externalHostBindings"], err = json.Marshal(publicBindings)
	return err
}
