package stackkitcommand

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

var ErrPlanExecutionBindingMissing = errors.New("the inspected plan has no native local Owner binding projection")
var ErrPlanLocalOwnerUnavailable = errors.New("the inspected node has no local Owner mutation capability")

var externalExecutionChannelPattern = regexp.MustCompile(`^execution-channel://sha256/[a-f0-9]{64}$`)

// PlanExecutionBinding is a read fact from the node's admitted PLAN, never a
// new execution grant or a controller-created channel.
type PlanExecutionBinding struct {
	SiteRef             string `json:"site_ref"`
	NodeRef             string `json:"node_ref"`
	ExecutionChannelRef string `json:"execution_channel_ref"`
	PlanHash            string `json:"plan_hash"`
	Source              string `json:"source"`
	OwnerRef            string `json:"owner_ref,omitempty"`
	OwnerKeyID          string `json:"owner_key_id,omitempty"`
}

func (b PlanExecutionBinding) Validate() error {
	if b.SiteRef == "" || b.NodeRef == "" || b.ExecutionChannelRef == "" ||
		b.SiteRef != strings.TrimSpace(b.SiteRef) || b.NodeRef != strings.TrimSpace(b.NodeRef) ||
		b.ExecutionChannelRef != strings.TrimSpace(b.ExecutionChannelRef) || !specHashPattern.MatchString(b.PlanHash) {
		return fmt.Errorf("plan execution binding requires its exact site, node, channel and plan hash")
	}
	if b.Source != "native-local-owner" && b.Source != "known-kit-binding" {
		return fmt.Errorf("plan execution binding source is invalid")
	}
	if b.Source == "native-local-owner" && (b.OwnerRef == "" || b.OwnerKeyID == "") {
		return fmt.Errorf("native local Owner identity is missing")
	}
	return nil
}

// ManagedPlanExecutionBinding consumes the same inspected artifact projection
// as ManagedBackupPlan. Only the verified native Owner can identify its local
// channel; absent older projections never authorize an arbitrary joined node.
func ManagedPlanExecutionBinding(result *agentpb.StackKitResult, instance, nodeRef, ownerRef string) (PlanExecutionBinding, error) {
	plan, hash, err := readManagedInspectedPlan(result, instance, true)
	if err != nil {
		return PlanExecutionBinding{}, err
	}
	binding := PlanExecutionBinding{NodeRef: nodeRef, PlanHash: hash, Source: "native-local-owner"}
	var envelope struct {
		Data struct {
			Output string `json:"output"`
		} `json:"data"`
	}
	var inspection struct {
		LocalOwner *struct {
			State      string `json:"state"`
			ReasonCode string `json:"reasonCode"`
			OwnerRef   string `json:"ownerRef"`
			KeyID      string `json:"keyId"`
			StackID    string `json:"stackId"`
			PlanHash   string `json:"planHash"`
			Binding    struct {
				SiteRef    string `json:"siteRef"`
				NodeRef    string `json:"nodeRef"`
				ChannelRef string `json:"executionChannelRef"`
			} `json:"binding"`
		} `json:"localOwner"`
	}
	if json.Unmarshal(result.CommandResultJson, &envelope) != nil || json.Unmarshal([]byte(envelope.Data.Output), &inspection) != nil {
		return binding, fmt.Errorf("local Owner Plan inspection is invalid")
	}
	if inspection.LocalOwner == nil {
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(envelope.Data.Output), &fields) != nil {
			return binding, fmt.Errorf("local Owner Plan inspection is invalid")
		}
		if _, supplied := fields["localOwner"]; supplied {
			return binding, fmt.Errorf("local Owner Plan projection is null")
		}
		return binding, ErrPlanExecutionBindingMissing
	}
	if plan == nil {
		return binding, fmt.Errorf("Guard did not attach the native local Owner Plan artifact projection")
	}
	local := inspection.LocalOwner
	if local.State == "unavailable" && local.StackID == instance && local.PlanHash == hash {
		return binding, fmt.Errorf("%w: %s", ErrPlanLocalOwnerUnavailable, local.ReasonCode)
	}
	if local.State != "verified" || ownerRef == "" || local.OwnerRef != ownerRef || local.KeyID == "" || local.StackID != instance || local.PlanHash != hash || local.Binding.NodeRef != nodeRef {
		return binding, fmt.Errorf("verified local Owner differs from the admitted Owner, StackInstance, Plan or canonical node")
	}
	binding.SiteRef, binding.ExecutionChannelRef, binding.OwnerRef, binding.OwnerKeyID = local.Binding.SiteRef, local.Binding.ChannelRef, local.OwnerRef, local.KeyID
	var nodes []struct {
		ID      string `json:"id"`
		SiteRef string `json:"siteRef"`
	}
	var external map[string]struct {
		NodeRef             string `json:"nodeRef"`
		ExecutionChannelRef string `json:"executionChannelRef"`
	}
	if len(plan["nodes"]) == 0 || len(plan["externalHostBindings"]) == 0 {
		return binding, fmt.Errorf("Guard did not attest the inspected Plan node projection")
	}
	if json.Unmarshal(plan["nodes"], &nodes) != nil || json.Unmarshal(plan["externalHostBindings"], &external) != nil {
		return binding, fmt.Errorf("inspected plan execution projection is invalid")
	}
	matched := false
	for _, node := range nodes {
		if node.ID == nodeRef {
			if matched || node.SiteRef != binding.SiteRef {
				return binding, fmt.Errorf("inspected plan has duplicate node identity")
			}
			matched = true
		}
	}
	if !matched {
		return binding, fmt.Errorf("local Owner names no inspected Plan node")
	}
	value, found := external[nodeRef]
	if found && (value.NodeRef != nodeRef || value.ExecutionChannelRef != binding.ExecutionChannelRef || !externalExecutionChannelPattern.MatchString(value.ExecutionChannelRef)) {
		return binding, fmt.Errorf("external execution binding substitutes another node")
	}
	return binding, binding.Validate()
}
