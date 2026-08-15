package jobs

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/pkg/core"
)

func parseWorkersFromPayload(payload any) ([]core.Worker, error) {
	if payload == nil {
		return nil, nil
	}

	items, ok := payload.([]interface{})
	if !ok {
		if maps, ok2 := payload.([]map[string]interface{}); ok2 {
			items = make([]interface{}, 0, len(maps))
			for _, item := range maps {
				items = append(items, item)
			}
			ok = true
		}
	}
	if !ok {
		// Allow direct []core.Worker in memory (no json roundtrip)
		if ws, ok2 := payload.([]core.Worker); ok2 {
			return ws, nil
		}
		return nil, fmt.Errorf("workers must be an array")
	}

	workers := make([]core.Worker, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("worker entry must be an object")
		}
		w := core.Worker{}
		if v, ok := m["id"].(string); ok {
			w.ID = v
		}
		if v, ok := m["name"].(string); ok {
			w.Name = v
		}
		if v, ok := m["type"].(string); ok {
			w.Type = v
		}
		if v, ok := m[providerField].(string); ok {
			w.Provider = v
		}
		if v, ok := m["status"].(string); ok {
			w.Status = v
		}
		// Capabilities
		if capsAny, ok := m["capabilities"].(map[string]interface{}); ok {
			if cpu, ok := payloadInt(capsAny["cpu"]); ok {
				w.Capabilities.CPU = cpu
			}
			if ram, ok := payloadInt(capsAny["ram"]); ok {
				w.Capabilities.RAM = ram
			}
			if disk, ok := payloadInt(capsAny["disk"]); ok {
				w.Capabilities.Disk = disk
			}
			if arch, ok := capsAny["arch"].(string); ok {
				w.Capabilities.Arch = arch
			}
			if osName, ok := capsAny["os"].(string); ok {
				w.Capabilities.OS = osName
			}
			if dv, ok := capsAny["dockerVersion"].(string); ok {
				w.Capabilities.DockerVersion = dv
			}
			if nvme, ok := capsAny["hasNVMe"].(bool); ok {
				w.Capabilities.HasNVMe = nvme
			}
			if hwt, ok := capsAny["hasHWTranscode"].(bool); ok {
				w.Capabilities.HasHWTranscode = hwt
			}
		}
		workers = append(workers, w)
	}

	return workers, nil
}

//nolint:gocyclo // Accepts mixed JSON/env numeric payload shapes from UI, jobs, and stored metadata.
func payloadInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int8:
		return int(v), true
	case int16:
		return int(v), true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case uint:
		return int(v), true
	case uint8:
		return int(v), true
	case uint16:
		return int(v), true
	case uint32:
		return int(v), true
	case uint64:
		return int(v), true
	case float32:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i), true
		}
		if f, err := v.Float64(); err == nil {
			return int(f), true
		}
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		if i, err := strconv.Atoi(trimmed); err == nil {
			return i, true
		}
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return int(f), true
		}
	}
	return 0, false
}

func workersSatisfyRequirements(req core.WorkerRequirements, workers []core.Worker) (bool, string) {
	if req.MinCloudServers == 0 && req.MinLocalServers == 0 {
		// Default to at least one worker if unspecified.
		req.MinLocalServers = 1
	}

	local := 0
	cloud := 0
	for _, w := range workers {
		provider := strings.ToLower(strings.TrimSpace(w.Provider))
		switch provider {
		case "local", "bare-metal", "", "onprem":
			local++
		default:
			cloud++
		}
	}

	if local < req.MinLocalServers {
		return false, fmt.Sprintf("need at least %d local worker(s), have %d", req.MinLocalServers, local)
	}
	if cloud < req.MinCloudServers {
		return false, fmt.Sprintf("need at least %d cloud worker(s), have %d", req.MinCloudServers, cloud)
	}

	return true, ""
}

func payloadString(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func payloadBool(payload map[string]interface{}, key string) bool {
	if payload == nil {
		return false
	}
	switch value := payload[key].(type) {
	case bool:
		return value
	case string:
		parsed, err := strconv.ParseBool(value)
		return err == nil && parsed
	default:
		return false
	}
}

func ownerSpecBootstrapFromPayload(payload map[string]interface{}) *OwnerSpecBootstrap {
	if payload == nil {
		return nil
	}
	switch value := payload["owner_spec_bootstrap"].(type) {
	case *OwnerSpecBootstrap:
		return normalizeOwnerSpecBootstrap(value)
	case OwnerSpecBootstrap:
		return normalizeOwnerSpecBootstrap(&value)
	case map[string]interface{}:
		return normalizeOwnerSpecBootstrap(&OwnerSpecBootstrap{
			Endpoint:  payloadMapString(value, "endpoint"),
			Token:     payloadMapString(value, "token"),
			ExpiresAt: payloadMapString(value, "expires_at"),
			Scopes:    payloadMapStringSlice(value, "scopes"),
		})
	default:
		return nil
	}
}

func payloadMapString(payload map[string]interface{}, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func payloadMapStringSlice(payload map[string]interface{}, key string) []string {
	switch values := payload[key].(type) {
	case []string:
		return values
	case []interface{}:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func cloneJobResult(result map[string]interface{}) map[string]interface{} {
	return cloneJobMap(result)
}

func mergeJobResultDefaults(job *Job, defaults map[string]interface{}) {
	job.mutateResult(func(result map[string]interface{}) {
		for key, value := range defaults {
			if _, ok := result[key]; !ok {
				result[key] = value
			}
		}
	})
}

func copyStringMetadata(result map[string]interface{}, metadata map[string]string, key string) {
	if result == nil || metadata == nil {
		return
	}
	if value := strings.TrimSpace(metadata[key]); value != "" {
		result[key] = value
	}
}

func copyBoolMetadata(result map[string]interface{}, metadata map[string]string, key string) {
	if result == nil || metadata == nil {
		return
	}
	value, ok := metadata[key]
	if !ok {
		return
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err == nil {
		result[key] = parsed
	}
}
