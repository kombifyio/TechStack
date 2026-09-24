package controlplane

import (
	"encoding/json"
	"time"
)

type rowScanner interface {
	Scan(dest ...any) error
}

func marshalObject(value map[string]any) ([]byte, error) {
	if len(value) == 0 {
		return []byte(`{}`), nil
	}
	return json.Marshal(value)
}

func marshalArray(value []map[string]any) ([]byte, error) {
	if len(value) == 0 {
		return []byte(`[]`), nil
	}
	return json.Marshal(value)
}

func decodeObject(payload []byte, out *map[string]any) error {
	if len(payload) == 0 {
		*out = map[string]any{}
		return nil
	}
	return json.Unmarshal(payload, out)
}

func decodeArray(payload []byte, out *[]map[string]any) error {
	if len(payload) == 0 {
		*out = nil
		return nil
	}
	return json.Unmarshal(payload, out)
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}
