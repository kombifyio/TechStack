package stackkitrelease

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// UseCaseResources are the release's per-workload quantities, in cores and GB.
// An omitted quantity remains undeclared; it is never supplied by a local table.
type UseCaseResources struct {
	CPUCores    float64 `json:"cpuCores,omitempty"`
	RAMGB       float64 `json:"ramGB,omitempty"`
	StorageGB   float64 `json:"storageGB,omitempty"`
	cpuDeclared bool
}

func (resources *UseCaseResources) UnmarshalJSON(data []byte) error {
	var values struct {
		CPUCores  *float64 `json:"cpuCores"`
		RAMGB     float64  `json:"ramGB"`
		StorageGB float64  `json:"storageGB"`
	}
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	*resources = UseCaseResources{RAMGB: values.RAMGB, StorageGB: values.StorageGB, cpuDeclared: values.CPUCores != nil}
	if values.CPUCores != nil {
		resources.CPUCores = *values.CPUCores
	}
	return nil
}

func (resources UseCaseResources) MarshalJSON() ([]byte, error) {
	var cpu *float64
	if resources.cpuDeclared || resources.CPUCores != 0 {
		cpu = &resources.CPUCores
	}
	return json.Marshal(struct {
		CPUCores  *float64 `json:"cpuCores,omitempty"`
		RAMGB     float64  `json:"ramGB,omitempty"`
		StorageGB float64  `json:"storageGB,omitempty"`
	}{cpu, resources.RAMGB, resources.StorageGB})
}

func (resources UseCaseResources) CPUMillicores() int {
	return int(math.Ceil(resources.CPUCores * 1000))
}
func (resources UseCaseResources) RAMMiB() int     { return int(math.Ceil(resources.RAMGB * 1024)) }
func (resources UseCaseResources) StorageGiB() int { return int(math.Ceil(resources.StorageGB)) }

type UseCaseResourceProvenance struct {
	Source string `json:"source"`
	Ref    string `json:"ref"`
}

type UseCaseHostFloor struct {
	MinCPUCores                    float64 `json:"minCpuCores,omitempty"`
	MinRAMGB                       float64 `json:"minRamGB,omitempty"`
	MinStorageGB                   float64 `json:"minStorageGB,omitempty"`
	MinAMD64MicroarchitectureLevel int     `json:"minAMD64MicroarchitectureLevel,omitempty"`
}

type UseCaseAcceleratorFacts struct {
	Vendor         string            `json:"vendor"`
	Access         string            `json:"access,omitempty"`
	Maturity       string            `json:"maturity,omitempty"`
	MinDriverMajor int               `json:"minDriverMajor,omitempty"`
	Reservation    *UseCaseResources `json:"reservation,omitempty"`
}

// WorkloadFootprint binds demand to its released alternative and module profile.
// It is catalog input, not a ResolvedPlan or an execution instruction.
type WorkloadFootprint struct {
	Workload                  string                   `json:"workload"`
	Alternative               string                   `json:"alternative"`
	Module                    string                   `json:"module"`
	ComputeProfile            string                   `json:"computeProfile"`
	HostFloor                 UseCaseHostFloor         `json:"hostFloor"`
	Reservation               UseCaseResources         `json:"reservation"`
	Recommended               *UseCaseResources        `json:"recommended,omitempty"`
	Accelerator               *UseCaseAcceleratorFacts `json:"accelerator,omitempty"`
	CPUReservationUnspecified bool                     `json:"cpuReservationUnspecified,omitempty"`
}

// WorkloadFootprints reads the selected modules. A default alternative may be
// substituted only by the release's own per-profile alternative declaration.
func (catalog UseCaseCatalog) WorkloadFootprints(workload, alternative, profile, accelerator string) ([]WorkloadFootprint, error) {
	if profile == "" {
		profile = "standard"
	}
	var alternatives []UseCaseAlternative
	for _, useCase := range catalog.UseCases {
		if useCase.ID == workload {
			alternatives = useCase.Alternatives
			if verdict, declared := useCase.ComputeTiers[profile]; declared && !verdict.Included {
				return nil, fmt.Errorf("release %s omits %s on profile %s: %s", catalog.Release.Tag, workload, profile, verdict.Reason)
			}
			if alternative == "" {
				alternative = useCase.DefaultAlternative
				if verdict, declared := useCase.ComputeTiers[profile]; declared {
					for _, candidate := range alternatives {
						if candidate.ID == verdict.ModuleSlug {
							alternative = candidate.ID
						}
					}
				}
			}
			break
		}
		for _, addOn := range useCase.AddOns {
			if addOn.ID == workload {
				alternatives = addOn.Alternatives
				if alternative == "" {
					alternative = addOn.DefaultAlternative
				}
				break
			}
		}
		if alternatives != nil {
			break
		}
	}
	for _, candidate := range alternatives {
		if candidate.ID != alternative {
			continue
		}
		if len(candidate.Modules) == 0 {
			break
		}
		result := make([]WorkloadFootprint, 0, len(candidate.Modules))
		for _, module := range candidate.Modules {
			facts, declared := module.ComputeProfileFacts[profile]
			if !declared || !facts.Executable || facts.HostFloor == nil || facts.Reservation == nil {
				return nil, fmt.Errorf("release %s has no executable footprint for %s/%s profile %s", catalog.Release.Tag, workload, module.ID, profile)
			}
			floor := *facts.HostFloor
			reservation := *facts.Reservation
			if !validResourceQuantities(floor.MinCPUCores, floor.MinRAMGB, floor.MinStorageGB, reservation.CPUCores, reservation.RAMGB, reservation.StorageGB) {
				return nil, fmt.Errorf("release %s has invalid resource quantities for %s", catalog.Release.Tag, module.ID)
			}
			footprint := WorkloadFootprint{Workload: workload, Alternative: alternative, Module: module.ID, ComputeProfile: profile, HostFloor: floor, Reservation: reservation, Recommended: facts.Recommended}
			footprint.CPUReservationUnspecified = !reservation.cpuDeclared
			if accelerator != "" && accelerator != "cpu" && len(module.AcceleratorProfileFacts) > 0 {
				gpu, declared := module.AcceleratorProfileFacts[accelerator]
				if !declared || gpu.Vendor == "" || gpu.Reservation == nil || !validResourceQuantities(gpu.Reservation.CPUCores, gpu.Reservation.RAMGB, gpu.Reservation.StorageGB) {
					return nil, fmt.Errorf("release %s has no usable %s accelerator footprint for %s", catalog.Release.Tag, accelerator, module.ID)
				}
				footprint.Accelerator = &gpu
				footprint.Reservation.CPUCores += gpu.Reservation.CPUCores
				footprint.Reservation.RAMGB += gpu.Reservation.RAMGB
				footprint.Reservation.StorageGB += gpu.Reservation.StorageGB
			}
			result = append(result, footprint)
		}
		return result, nil
	}
	return nil, fmt.Errorf("release %s has no footprint binding for workload %q alternative %q", catalog.Release.Tag, workload, alternative)
}

func validResourceQuantities(values ...float64) bool {
	for _, value := range values {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) || value > float64(math.MaxInt)/1024 {
			return false
		}
	}
	return true
}

// ServiceFootprints recognizes released application/module identities, leaving
// unrelated discovered applications to Techstack's generic sizing policy.
// matchWorkload permits a service name to be an explicit workload identity.
// Generic service types such as "media" are not use-case selections.
func (catalog UseCaseCatalog) ServiceFootprints(identity, profile string, matchWorkload bool) ([]WorkloadFootprint, bool, error) {
	identity = strings.ToLower(strings.TrimSpace(identity))
	for _, useCase := range catalog.UseCases {
		if matchWorkload && identity == useCase.ID {
			facts, err := catalog.WorkloadFootprints(useCase.ID, "", profile, "")
			return facts, true, err
		}
		workloads := []UseCaseAddOn{{ID: useCase.ID, Alternatives: useCase.Alternatives}}
		workloads = append(workloads, useCase.AddOns...)
		for _, workload := range workloads {
			for _, alternative := range workload.Alternatives {
				matches := identity == alternative.ID
				for _, module := range alternative.Modules {
					matches = matches || identity == module.ID
				}
				if matches {
					facts, err := catalog.WorkloadFootprints(workload.ID, alternative.ID, profile, "")
					return facts, true, err
				}
			}
		}
	}
	return nil, false, nil
}
