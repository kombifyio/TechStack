package routes_test

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/prometheus/client_golang/prometheus"
)

func TestMetrics_WorkflowLifecycleIsObservableWithoutRunLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := routes.NewMetrics(reg)
	m.ObserveRunTransition(workflow.TypeActionCardRemediation, workflow.RunRunning, workflow.RunCompleted, 2*time.Second)
	m.ObserveStepTransition(workflow.TypeActionCardRemediation, "verify", workflow.StepRunning, workflow.StepCompleted, 0, time.Second)
	m.ObserveTimer(workflow.TimerReminder, workflow.TimerOutcomeDelivered)

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"techstack_ril_workflow_run_transitions_total":  false,
		"techstack_ril_workflow_step_transitions_total": false,
		"techstack_ril_workflow_timers_total":           false,
	}
	for _, family := range families {
		if _, ok := want[family.GetName()]; ok {
			want[family.GetName()] = true
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "run_id" || label.GetName() == "owner_id" || label.GetName() == "tenant_id" {
						t.Fatalf("workflow metric leaked high-cardinality label %q", label.GetName())
					}
				}
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("workflow metric %s was not emitted", name)
		}
	}
}

func TestMetrics_MonthlyRuntimeEnrollmentMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := routes.NewMetrics(reg)

	m.MonthlyRuntimeEnrollments.WithLabelValues("pending").Set(2)
	m.MonthlyRuntimeEnrollments.WithLabelValues("retrying").Set(1)
	m.MonthlyRuntimeEnrollments.WithLabelValues("failed").Set(3)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	found := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetName() != "techstack_monthly_runtime_enrollments" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			status := ""
			for _, label := range metric.GetLabel() {
				if label.GetName() == "status" {
					status = label.GetValue()
				}
			}
			found[status] = metric.GetGauge().GetValue()
		}
	}
	for status, want := range map[string]float64{"pending": 2, "retrying": 1, "failed": 3} {
		if found[status] != want {
			t.Fatalf("monthly runtime enrollment metric %s = %v, want %v (all=%v)", status, found[status], want, found)
		}
	}
}
