package specv2

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

var repairInstallZone = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.kombify\.me$`)

// RequireRolloutRepairIntent bounds recovery to the same already admitted
// workloads. The pinned CLI remains the schema and module dependency authority;
// this custody boundary prevents recovery from replacing customer intent.
func RequireRolloutRepairIntent(baseline, candidate map[string]any) error {
	if RequireCanonicalV2(baseline) != nil || RequireCanonicalV2(candidate) != nil {
		return fmt.Errorf("repair requires canonical original and candidate specs")
	}
	prior := deepCopyValue(baseline).(map[string]any)
	next := deepCopyValue(candidate).(map[string]any)
	priorNetwork, _ := prior["network"].(map[string]any)
	nextNetwork, _ := next["network"].(map[string]any)
	priorDomain, _ := priorNetwork["domain"].(map[string]any)
	nextDomain, _ := nextNetwork["domain"].(map[string]any)
	priorBase, _ := priorDomain["base"].(string)
	nextBase, _ := nextDomain["base"].(string)
	bindZone := priorBase == "kombify.me" && repairInstallZone.MatchString(nextBase)
	if priorBase != nextBase && !bindZone {
		return fmt.Errorf("repair cannot replace the installation domain")
	}
	if nextDomain != nil {
		nextDomain["base"] = priorDomain["base"]
	}
	modules, _ := baseline["modules"].(map[string]any)
	priorRoutes, _ := prior["routes"].(map[string]any)
	nextRoutes, _ := next["routes"].(map[string]any)
	policies, services := map[string]bool{}, map[string]bool{}
	for ref, raw := range nextRoutes {
		route, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("repair route is invalid")
		}
		service, _ := route["serviceRef"].(string)
		if existing, found := priorRoutes[ref]; found {
			owned, ok := existing.(map[string]any)
			if !ok {
				return fmt.Errorf("original route is invalid")
			}
			if bindZone && route["host"] != owned["host"] {
				if service == "" || strings.Contains(service, ".") || route["host"] != service+"."+nextBase {
					return fmt.Errorf("repair route escapes its installation zone")
				}
				route["host"] = owned["host"]
			}
			if !reflect.DeepEqual(route, owned) {
				return fmt.Errorf("repair cannot replace an existing route")
			}
			continue
		}
		module, _ := route["moduleRef"].(string)
		policy, _ := route["accessPolicyRef"].(string)
		if module == "" || modules[module] == nil || service == "" || policy == "" {
			return fmt.Errorf("repair route must belong to an already selected module")
		}
		services[service], policies[policy] = true, true
		delete(nextRoutes, ref)
	}
	if !reflect.DeepEqual(priorRoutes, nextRoutes) {
		return fmt.Errorf("repair cannot remove an existing route")
	}
	if err := repairAddedEntries(prior, next, "access", policies); err != nil {
		return err
	}
	priorData, _ := prior["data"].(map[string]any)
	nextData, _ := next["data"].(map[string]any)
	if err := repairAddedEntries(priorData, nextData, "bindings", services); err != nil {
		return err
	}
	if len(nextData) == 0 && len(priorData) == 0 {
		delete(prior, "data")
		delete(next, "data")
	}
	if !reflect.DeepEqual(prior, next) {
		return fmt.Errorf("repair cannot replace admitted workload, node, identity, or custody intent")
	}
	return nil
}

func repairAddedEntries(prior, next map[string]any, section string, allowed map[string]bool) error {
	owned, _ := prior[section].(map[string]any)
	entries, _ := next[section].(map[string]any)
	for ref := range entries {
		if _, exists := owned[ref]; !exists {
			if !allowed[ref] {
				return fmt.Errorf("repair adds unrelated dependency intent")
			}
			delete(entries, ref)
		}
	}
	if len(entries) == 0 && len(owned) == 0 {
		delete(prior, section)
		delete(next, section)
		return nil
	}
	if !reflect.DeepEqual(owned, entries) {
		return fmt.Errorf("repair cannot replace existing dependency intent")
	}
	return nil
}
