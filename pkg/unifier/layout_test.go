package unifier

import (
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

// A use case and its add-on workloads are one placed unit, so an add-on can
// never land away from its primary (StackKits rejects such a split).
func TestFootprintDemandsGroupAddOnsWithTheirUseCase(t *testing.T) {
	catalog := stackkitrelease.UseCaseCatalog{UseCases: []stackkitrelease.UseCase{
		{ID: "photos", Title: "Photos", AddOns: []stackkitrelease.UseCaseAddOn{{ID: "photos-share"}}},
	}}
	demands := footprintDemands(catalog, []stackkitrelease.WorkloadFootprint{
		{Workload: "photos", Module: "stackkits-immich-runtime", ReservationProvenance: "measured", Reservation: stackkitrelease.UseCaseResources{RAMGB: 2}},
		{Workload: "photos-share", Module: "stackkits-photos-share-runtime", ReservationProvenance: "measured", Reservation: stackkitrelease.UseCaseResources{RAMGB: 0.5}},
	})
	if len(demands) != 1 || demands[0].Workload != "photos" || len(demands[0].Workloads) != 2 || demands[0].Reservation.RAMGB != 2.5 {
		t.Fatalf("want one photos unit with both workloads, got %+v", demands)
	}
}
