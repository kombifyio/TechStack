package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/portinventory"
)

const portLifecycleReconciliationTimeout = 5 * time.Second

func deployApplyEnabled(job *Job) bool {
	if job == nil || job.Payload == nil {
		return true
	}
	if value, ok := job.Payload["apply"].(bool); ok {
		return value
	}
	if value, ok := job.Payload["apply"].(string); ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	}
	return true
}

// runPortGovernedLifecycle keeps port admission and runtime mutation in one
// explicit ordering seam: simulation -> evaluate/admit -> mutation fence ->
// bootstrap/apply -> verify -> activate.
func (r *deployRollout) runPortGovernedLifecycle(ctx context.Context) (applied bool, lifecycleErr error) {
	if err := r.runSimulationGate(ctx); err != nil {
		return false, err
	}
	apply := deployApplyEnabled(r.job)
	if err := r.admitRuntimeListeners(ctx, apply); err != nil {
		return false, err
	}
	if !apply {
		return false, nil
	}
	if r.portGeneration != nil {
		defer func() {
			lifecycleErr = r.reconcilePortLifecycle(ctx, lifecycleErr)
		}()
	}
	if r.portActivated {
		if err := r.runVerify(ctx); err != nil {
			return true, err
		}
		return true, nil
	}
	if err := r.runRollout(ctx); err != nil {
		return true, err
	}
	if err := r.runVerify(ctx); err != nil {
		return true, err
	}
	return true, nil
}

func (r *deployRollout) admitRuntimeListeners(ctx context.Context, apply bool) error {
	if r == nil || r.cfg == nil {
		return wrapProvisionError(StepPortAdmission,
			"runtime listener authority is not configured",
			"Techstack cannot evaluate or reserve compiler-declared host listeners. No host mutation was started.")
	}
	enrollment := r.actionReq.TechStackEnrollment
	if enrollment == nil || strings.TrimSpace(enrollment.TenantID) == "" || strings.TrimSpace(enrollment.ServerID) == "" {
		return wrapProvisionError(StepPortAdmission,
			"runtime listener admission requires an exact tenant-owned RuntimeServer",
			"Techstack could not bind the compiler plan to a canonical runtime server. No host mutation was started.")
	}
	document, err := readResolvedPlanForPortAdmission(r.actionReq.UnifiedPath)
	if err != nil {
		return wrapProvisionError(StepPortAdmission, err.Error(), "Techstack could not read the canonical compiler plan. No host mutation was started.")
	}
	listenerSet, projectionErr := parseResolvedPlanListenerSet(document)
	if listenerSet.PlanHash == "" {
		reason := "canonical ResolvedPlan identity is incomplete"
		if projectionErr != nil {
			reason = projectionErr.Error()
		}
		return wrapProvisionError(StepPortAdmission, reason, "Only a canonical, hash-bound ResolvedPlan can authorize StackKits apply. No fallback identity was used.")
	}
	if !resolvedPlanHashPattern.MatchString(r.generatedPlanHash) || r.generatedPlanHash != listenerSet.PlanHash {
		return wrapProvisionError(StepPortAdmission,
			"canonical ResolvedPlan hash does not match the pinned artifact generation receipt",
			"Regenerate the StackKit artifacts before reserving host listeners. No host mutation was started.")
	}
	if projectionErr != nil {
		r.continueWithoutPortProjection(listenerSet, projectionErr)
		return nil
	}
	if err = listenerSet.requireSingleRuntimeTarget(r.actionReq.PlatformNodes); err != nil {
		r.continueWithoutPortProjection(listenerSet, err)
		return nil
	}
	if r.cfg.PortInventory == nil {
		if len(listenerSet.Listeners) == 0 {
			return nil
		}
		return wrapProvisionError(StepPortAdmission,
			"runtime listener authority is not configured",
			"Techstack cannot evaluate or reserve compiler-declared host listeners. No host mutation was started.")
	}
	request := portinventory.CurrentAdmissionRequest{
		TenantID: strings.TrimSpace(enrollment.TenantID), ServerID: strings.TrimSpace(enrollment.ServerID),
		OwnerSubjectID: strings.TrimSpace(enrollment.OwnerID),
		StackID:        strings.TrimSpace(r.actionReq.StackID), ResolvedPlanHash: listenerSet.PlanHash, Requirements: listenerSet.portRequirements(),
	}
	if !apply {
		_, err = r.cfg.PortInventory.EvaluateCurrent(ctx, request)
		return r.portAdmissionError(err)
	}
	admission, err := r.cfg.PortInventory.AdmitCurrent(ctx, request)
	if err != nil {
		return r.portAdmissionError(err)
	}
	r.portGeneration = &admission.GenerationRef
	r.portActivated = admission.State == portinventory.ClaimStateActive
	r.job.mutateResult(func(result map[string]interface{}) {
		result["runtime_listener_plan_hash"] = admission.GenerationRef.ResolvedPlanHash
		result["runtime_listener_server_generation"] = admission.GenerationRef.ServerGeneration
		result["runtime_listener_claim_count"] = len(admission.Admission.Claims)
	})
	return nil
}

// continueWithoutPortProjection keeps Techstack's optional port inventory out
// of the StackKits execution authority. A valid, hash-bound ResolvedPlan may
// use a custom or newer listener shape that this Techstack release cannot
// project yet. That makes port visibility unavailable; it does not make the
// user-owned StackKit invalid and must not stop apply.
func (r *deployRollout) continueWithoutPortProjection(set resolvedPlanListenerSet, projectionErr error) {
	reason := "runtime listener projection is unavailable"
	if projectionErr != nil {
		reason = projectionErr.Error()
	}
	r.job.mutateResult(func(result map[string]interface{}) {
		result["runtime_listener_admission"] = map[string]interface{}{
			"status":               "unavailable",
			"reason_code":          "optional_projection_unavailable",
			"reason":               reason,
			"stackkit_instance_id": set.StackKitInstanceID,
			"plan_hash":            set.PlanHash,
		}
	})
	r.q.addLog(r.job, "warn", "Techstack port projection is unavailable; continuing with StackKits-owned apply")
}

func (set resolvedPlanListenerSet) portRequirements() []portinventory.Requirement {
	requirements := make([]portinventory.Requirement, 0, len(set.Listeners))
	for _, listener := range set.Listeners {
		requirements = append(requirements, portinventory.Requirement{
			ID: listener.ID, NodeRef: listener.NodeRef, Transport: portinventory.Transport(listener.Transport),
			BindAddress: listener.BindAddress, Port: listener.Port, Sharing: portinventory.Sharing(listener.Sharing),
			ListenerGroupRef: listener.ListenerGroupRef, Exposure: portinventory.Exposure(listener.Exposure),
			SourceRouteRefs: append([]string(nil), listener.SourceRouteRefs...),
		})
	}
	return requirements
}

func (set resolvedPlanListenerSet) requireSingleRuntimeTarget(nodes []PlatformNode) error {
	if len(set.Listeners) == 0 {
		return nil
	}
	normalized := normalizePlatformNodes(nodes)
	if len(normalized) != 1 || normalized[0].Name != set.NodeRef {
		return fmt.Errorf("runtime listener nodeRef %q does not select exactly one rollout node", set.NodeRef)
	}
	return nil
}

func readResolvedPlanForPortAdmission(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("runtime listener admission requires the canonical ResolvedPlan path")
	}
	file, err := os.Open(path) // #nosec G304 -- pinned StackKits generator output selected by DeployHandler.
	if err != nil {
		return nil, fmt.Errorf("open canonical ResolvedPlan for runtime listener admission: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxResolvedPlanListenerDocumentLen {
		return nil, errors.New("canonical ResolvedPlan for runtime listener admission must be a non-empty bounded regular file")
	}
	document := make([]byte, info.Size())
	if _, err = io.ReadFull(file, document); err != nil {
		return nil, fmt.Errorf("read canonical ResolvedPlan for runtime listener admission: %w", err)
	}
	return document, nil
}

func (r *deployRollout) markPortMutationStarted(ctx context.Context) error {
	if r == nil || r.portGeneration == nil || r.cfg == nil || r.cfg.PortInventory == nil {
		return nil
	}
	if err := r.cfg.PortInventory.MarkMutationStarted(ctx, *r.portGeneration); err != nil {
		return err
	}
	r.portMutationStarted = true
	return nil
}

func (r *deployRollout) activatePortClaims(ctx context.Context) error {
	if r == nil || r.portGeneration == nil || r.portActivated || r.cfg == nil || r.cfg.PortInventory == nil {
		return nil
	}
	if err := r.cfg.PortInventory.Activate(ctx, *r.portGeneration); err != nil {
		return err
	}
	r.portActivated = true
	return nil
}

func (r *deployRollout) reconcilePortLifecycle(ctx context.Context, lifecycleErr error) error {
	if r == nil || r.portGeneration == nil || r.portActivated || r.cfg == nil || r.cfg.PortInventory == nil {
		return lifecycleErr
	}
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), portLifecycleReconciliationTimeout)
	defer cancel()
	var err error
	if r.portMutationStarted {
		err = r.cfg.PortInventory.MarkUncertain(reconcileCtx, *r.portGeneration)
	} else {
		err = r.cfg.PortInventory.AbortBeforeMutation(reconcileCtx, *r.portGeneration)
	}
	if err == nil {
		return lifecycleErr
	}
	reconcileErr := fmt.Errorf("reconcile runtime listener claim generation: %w", err)
	if lifecycleErr == nil {
		return reconcileErr
	}
	return errors.Join(lifecycleErr, reconcileErr)
}

func (r *deployRollout) portAdmissionError(err error) error {
	if err == nil {
		return nil
	}
	var conflict *portinventory.ConflictError
	if errors.As(err, &conflict) {
		r.job.mutateResult(func(result map[string]interface{}) {
			result["port_admission_error"] = map[string]interface{}{
				"error_code": conflict.ErrorCode, "reason_code": conflict.ReasonCode, "retryable": conflict.Retryable,
				"transport": conflict.Transport, "bind_address": conflict.BindAddress, "port": conflict.Port,
				"user_guidance": conflict.UserGuidance,
			}
		})
		return wrapProvisionError(StepPortAdmission, conflict.Error(), conflict.UserGuidance.Body)
	}
	return wrapProvisionError(StepPortAdmission, fmt.Sprintf("runtime listener admission failed: %v", err),
		"Techstack could not reserve the compiler-declared host listeners. No host mutation was started.")
}
