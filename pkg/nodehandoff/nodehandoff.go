package nodehandoff

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	KeyServerNodeRole         = "server_node_role"
	KeyRequestedServices      = "requested_services"
	KeyServerRemoteHost       = "server_remote_host"
	KeyServerRemoteUser       = "server_remote_user"
	KeyServerRemotePort       = "server_remote_port"
	KeyServerRemoteAuthMethod = "server_remote_auth_method"
	KeyServerRemoteCredential = "server_remote_credential_ref"
	KeyServerRemoteSSHKey     = "server_remote_ssh_key"
	KeyServerRemoteUseSudo    = "server_remote_use_sudo"
)

func NormalizeNodeRole(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "foundation", "main", "standalone", "control-plane", "control_plane":
		return "foundation"
	case "storage":
		return "storage"
	default:
		return "worker"
	}
}

func WorkerTypeForNodeRole(value string) string {
	switch NormalizeNodeRole(value) {
	case "foundation":
		return "main"
	case "storage":
		return "storage"
	default:
		return "worker"
	}
}

func NodeRoleFromWorkerType(value string) string {
	return NormalizeNodeRole(value)
}

func NormalizeServiceKeys(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

func ServiceKeysFromAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		return NormalizeServiceKeys(typed)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
		return NormalizeServiceKeys(out)
	case string:
		parts := strings.FieldsFunc(typed, func(r rune) bool {
			return r == ',' || r == ';' || r == '|'
		})
		return NormalizeServiceKeys(parts)
	default:
		return nil
	}
}

func StringFromMap(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func IntFromMap(values map[string]any, key string) int {
	if len(values) == 0 {
		return 0
	}
	switch value := values[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil {
			return parsed
		}
	}
	return 0
}

func BoolFromMap(values map[string]any, key string) bool {
	if len(values) == 0 {
		return false
	}
	switch value := values[key].(type) {
	case bool:
		return value
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		return err == nil && parsed
	default:
		return false
	}
}

func MergeTags(raw string, metadata map[string]any) string {
	parts := []string{}
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		parts = append(parts, value)
	}
	for _, part := range strings.Split(raw, ",") {
		add(part)
	}
	if role := StringFromMap(metadata, KeyServerNodeRole); role != "" {
		add(KeyServerNodeRole + "=" + NormalizeNodeRole(role))
	}
	if services := ServiceKeysFromAny(metadata[KeyRequestedServices]); len(services) > 0 {
		add(KeyRequestedServices + "=" + strings.Join(services, "|"))
	}
	for _, key := range []string{KeyServerRemoteHost, KeyServerRemoteUser, KeyServerRemoteAuthMethod, KeyServerRemoteCredential, KeyServerRemoteSSHKey} {
		if value := StringFromMap(metadata, key); value != "" {
			add(key + "=" + value)
		}
	}
	if port := IntFromMap(metadata, KeyServerRemotePort); port > 0 {
		add(KeyServerRemotePort + "=" + strconv.Itoa(port))
	}
	if BoolFromMap(metadata, KeyServerRemoteUseSudo) {
		add(KeyServerRemoteUseSudo + "=true")
	}
	return strings.Join(parts, ",")
}

func MetadataFromTags(raw string) map[string]any {
	out := map[string]any{}
	for _, part := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		switch key {
		case KeyServerNodeRole:
			out[key] = NormalizeNodeRole(value)
		case KeyRequestedServices:
			out[key] = ServiceKeysFromAny(value)
		case KeyServerRemotePort:
			if parsed, err := strconv.Atoi(value); err == nil {
				out[key] = parsed
			}
		case KeyServerRemoteUseSudo:
			if parsed, err := strconv.ParseBool(value); err == nil {
				out[key] = parsed
			}
		default:
			out[key] = value
		}
	}
	return out
}

func MergeMetadata(primary, fallback map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range fallback {
		if !emptyValue(value) {
			out[key] = value
		}
	}
	for key, value := range primary {
		if !emptyValue(value) {
			out[key] = value
		}
	}
	return out
}

func emptyValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []string:
		return len(NormalizeServiceKeys(typed)) == 0
	case []any:
		return len(typed) == 0
	default:
		return false
	}
}
