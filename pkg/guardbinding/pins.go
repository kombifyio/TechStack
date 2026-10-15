package guardbinding

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// PinFile is the root-only file in the executor state directory that holds the
// binding keys the privileged executor verifies against. Only the layout
// command writes it, from an enrollment root still owns; the Guard daemon's
// user can neither write nor replace it.
const PinFile = "binding-signing-keys.json"

type pinFile struct {
	Keys []PinnedKey `json:"keys"`
}

// EncodePins is the PinFile content for keys; no keys pins nothing.
func EncodePins(keys []PinnedKey) ([]byte, error) {
	if keys == nil {
		keys = []PinnedKey{}
	}
	return json.Marshal(pinFile{Keys: keys})
}

// ReadPins reads a PinFile. A missing file pins nothing, as for a Guard paired
// before binding keys existed. A file that cannot be read or parsed is an
// error, so a damaged pin set never silently admits unsigned bindings.
func ReadPins(path string) ([]PinnedKey, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the executor's own fixed state path.
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file pinFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("pinned binding keys are malformed: %w", err)
	}
	return file.Keys, nil
}
